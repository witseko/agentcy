package tokens

import (
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/witseko/agentcy/internal/paths"
	"github.com/witseko/agentcy/internal/progress"
)

// mustParse parses settings that must be valid.
func mustParse(t *testing.T, text string) *Settings {
	t.Helper()
	settings, err := ParseSettings([]byte(text))
	if err != nil {
		t.Fatal(err)
	}
	return settings
}

// text returns a pointer to a string.
func text(value string) *string {
	return &value
}

// reported is a slot with exact usage, as RoomUsage returns it.
func reported(model, source string, input, output int64) SlotUsage {
	total := input + output
	return SlotUsage{
		Model: text(model), InputTokens: input, OutputTokens: output, ConversationTokens: total,
		Source: source, ActualTokens: &total, HasUsage: true,
	}
}

// balanceMember builds a status member for the balancer.
func balanceMember(name, role, state string, usage *MemberUsage) BalanceMember {
	return BalanceMember{Member: progress.Member{Name: name, Role: role, State: state}, Usage: usage}
}

// Ported from test_unequal_starting_budgets_balance_by_remaining_percentage.
func TestUnequalBudgetsBalanceByRemainingPercentage(t *testing.T) {
	settings := mustParse(t, `{"models": {"claude-sonnet-4-6": {"initial_tokens": 100000}, "gemini-2.5-pro": {"initial_tokens": 1000000}}}`)
	spend := map[string]int64{"claude-sonnet-4-6": 50_000, "gemini-2.5-pro": 100_000}
	claude := BuildMemberUsage(reported("claude-sonnet-4-6", SourceReported, 30_000, 20_000), settings, spend)
	gemini := BuildMemberUsage(reported("gemini-2.5-pro", SourcePlugin, 70_000, 30_000), settings, spend)
	if claude.Budget.UsedPercent != 50.0 || gemini.Budget.UsedPercent != 10.0 {
		t.Fatalf("used percent = %v and %v", claude.Budget.UsedPercent, gemini.Budget.UsedPercent)
	}
	balance := EvaluateBalance([]BalanceMember{
		balanceMember("Claude", "lead", "replied", claude),
		balanceMember("Gemini", "worker", "replied", gemini),
		balanceMember("Human", "human", "human", nil),
	}, settings)
	want := &Balance{
		Balanced: false, ImbalanceGapPercent: 40.0, Overloaded: []string{"Claude"}, Preferred: []string{"Gemini"},
		Recommendation: "Token balance: Claude is at 50.0% of budget (50.0% left) while Gemini is at 10.0% (90.0% left). Prefer assigning new tasks or handoffs to Gemini.",
	}
	if !reflect.DeepEqual(balance, want) {
		t.Errorf("balance = %+v", balance)
	}
}

// Ported from test_same_model_peers_are_not_recommended_against_each_other.
func TestSameModelPeersAreNotComparedWithEachOther(t *testing.T) {
	settings := mustParse(t, `{"models": {"claude-sonnet-4-6": {"initial_tokens": 100000}}}`)
	usage := BuildMemberUsage(reported("claude-sonnet-4-6", SourceReported, 60_000, 20_000), settings, map[string]int64{"claude-sonnet-4-6": 80_000})
	balance := EvaluateBalance([]BalanceMember{
		balanceMember("Lead", "lead", "replied", usage),
		balanceMember("Worker", "worker", "replied", usage),
	}, settings)
	if got := balance.Value().Compact(); got != `{"balanced":true,"imbalance_gap_percent":0.0}` {
		t.Errorf("balance = %s", got)
	}
}

// Ported from test_exhausted_model_emits_warning_and_ignores_retired_peers.
func TestExhaustedModelWarnsAndIgnoresRetiredPeers(t *testing.T) {
	settings := mustParse(t, `{"models": {"claude-sonnet-4-6": {"initial_tokens": 100000}, "gemini-2.5-pro": {"initial_tokens": 500000}, "zai/glm-4.5": {"initial_tokens": 500000}}}`)
	claude := BuildMemberUsage(reported("claude-sonnet-4-6", SourceReported, 80_000, 25_000), settings, map[string]int64{"claude-sonnet-4-6": 105_000})
	gemini := BuildMemberUsage(reported("gemini-2.5-pro", SourcePlugin, 10_000, 5_000), settings, map[string]int64{"gemini-2.5-pro": 15_000})
	glm := BuildMemberUsage(SlotUsage{Model: text("zai/glm-4.5"), Source: SourceEstimated}, settings, map[string]int64{})
	if claude.Budget.Status != StatusExhausted {
		t.Fatalf("status = %s", claude.Budget.Status)
	}
	balance := EvaluateBalance([]BalanceMember{
		balanceMember("Claude", "lead", "replied", claude),
		balanceMember("Gemini", "worker", "replied", gemini),
		balanceMember("GLM", "worker", "retired", glm),
	}, settings)
	if balance.Balanced || !reflect.DeepEqual(balance.Preferred, []string{"Gemini"}) {
		t.Fatalf("balance = %+v", balance)
	}
	want := "Token budget warning: Claude has reached 105.0% of its initial budget. Route follow-up tasks or handoffs to Gemini (97.0% left)."
	if balance.Recommendation != want {
		t.Errorf("recommendation = %q", balance.Recommendation)
	}
}

// TestMemberUsageFieldOrder checks Python's insertion order: actual_tokens and model
// after source, then the budget fields.
func TestMemberUsageFieldOrder(t *testing.T) {
	settings := mustParse(t, `{"default_initial_tokens": 1000}`)
	cases := []struct {
		raw  SlotUsage
		want string
	}{
		{
			reported("m", SourceReported, 3, 1),
			`{"conversation_tokens":4,"input_tokens":3,"output_tokens":1,"model_tokens_used":4,"estimated_tokens":0,"source":"reported","actual_tokens":4,"model":"m","initial_tokens":1000,"remaining_tokens":996,"used_percent":0.4,"remaining_percent":99.6,"status":"ok"}`,
		},
		{
			SlotUsage{InputTokens: 750, ConversationTokens: 750, EstimatedTokens: 750, Source: SourceEstimated, HasUsage: true},
			`{"conversation_tokens":750,"input_tokens":750,"output_tokens":0,"model_tokens_used":750,"estimated_tokens":750,"source":"estimated","initial_tokens":1000,"remaining_tokens":250,"used_percent":75.0,"remaining_percent":25.0,"status":"warning"}`,
		},
	}
	for _, test := range cases {
		if got := BuildMemberUsage(test.raw, settings, nil).Value().Compact(); got != test.want {
			t.Errorf("usage:\n got %s\nwant %s", got, test.want)
		}
	}
	noBudget := BuildMemberUsage(reported("m", SourcePlugin, 1, 2), nil, nil).Value().Compact()
	if want := `{"conversation_tokens":3,"input_tokens":1,"output_tokens":2,"model_tokens_used":3,"estimated_tokens":0,"source":"plugin","actual_tokens":3,"model":"m"}`; noBudget != want {
		t.Errorf("usage without budget: %s", noBudget)
	}
}

func TestBuildMemberUsageReturnsNilWithoutTracking(t *testing.T) {
	if usage := BuildMemberUsage(SlotUsage{Source: SourceEstimated, HasUsage: true}, nil, nil); usage != nil {
		t.Errorf("no settings, model, budget or exact usage: %+v", usage)
	}
	if usage := BuildMemberUsage(SlotUsage{Model: text("m"), Source: SourceEstimated}, nil, nil); usage != nil {
		t.Errorf("no usage and no budget: %+v", usage)
	}
}

func TestSumConversationUsage(t *testing.T) {
	estimated := SlotUsage{EstimatedTokens: 7, Source: SourceEstimated, HasUsage: true}
	slots := []SlotUsage{reported("m", SourceReported, 10, 5), estimated, {Source: SourceEstimated}}
	got := SumConversationUsage(slots).Value().Compact()
	if want := `{"total_tokens":22,"actual_tokens":15,"estimated_tokens":7,"actual_members":1,"estimated_members":1}`; got != want {
		t.Errorf("total = %s", got)
	}
	if total := SumConversationUsage([]SlotUsage{{Source: SourceEstimated}}); total != nil {
		t.Errorf("total without usage = %+v", total)
	}
}

func TestBudgetStatus(t *testing.T) {
	cases := []struct {
		used float64
		warn int64
		want string
	}{
		{100.0, 75, StatusExhausted}, {99.9, 75, StatusWarning}, {75.0, 75, StatusWarning}, {74.9, 75, StatusOK}, {150, 99, StatusExhausted},
	}
	for _, test := range cases {
		if got := BudgetStatus(test.used, test.warn); got != test.want {
			t.Errorf("BudgetStatus(%v, %d) = %s, want %s", test.used, test.warn, got, test.want)
		}
	}
}

// pythonBalance runs build_member_usage, evaluate_balance and conversation_usage on each
// case and prints the results as compact JSON in insertion order.
const pythonBalance = `
import json, sys
from agentcy.tokens.balancer import build_member_usage, conversation_usage, evaluate_balance
from agentcy.tokens.settings import TokenSettings
out = []
for case in json.load(sys.stdin):
    config = TokenSettings.model_validate_json(case["settings"]) if case["settings"] is not None else None
    members, usages = {}, []
    for member in case["members"]:
        usage = build_member_usage(member["raw"], config, case["spend"])
        usages.append(usage)
        entry = {"role": member["role"], "state": member["state"]}
        if member["launch"] != "absent":
            entry["launch_state"] = member["launch"]
        if usage is not None:
            entry["usage"] = usage
        members[member["name"]] = entry
    total = conversation_usage(member["raw"] for member in case["members"])
    out.append([usages, evaluate_balance(members, config), total])
print(json.dumps(out, ensure_ascii=False, separators=(",", ":")))
`

// randomCase is one balancer case for Python and Go.
type randomCase struct {
	Settings *string          `json:"settings"`
	Spend    map[string]int64 `json:"spend"`
	Members  []randomMember   `json:"members"`
}

// randomMember is one member of a randomCase. Launch is "absent", null or a state.
type randomMember struct {
	Name   string         `json:"name"`
	Role   string         `json:"role"`
	State  string         `json:"state"`
	Launch any            `json:"launch"`
	Raw    map[string]any `json:"raw"`
	slot   SlotUsage
}

// pick returns a random element.
func pick[T any](generator *rand.Rand, items ...T) T {
	return items[generator.IntN(len(items))]
}

// newRandomCase draws settings, ledger spend and up to five members.
func newRandomCase(generator *rand.Rand) randomCase {
	models := []string{"alpha", "Beta", "gamma-1", "delta"}
	var test randomCase
	if generator.IntN(4) > 0 {
		settings := fmt.Sprintf(`{"warn_percent": %d, "imbalance_gap_percent": %d, "models": {"alpha": {"initial_tokens": %d, "imbalance_gap_percent": %d}, "beta": {"initial_tokens": %d, "warn_percent": %d}}%s}`,
			1+generator.IntN(99), 5+generator.IntN(86), 1+generator.IntN(200000), 5+generator.IntN(86), 1+generator.IntN(5000), 1+generator.IntN(99),
			pick(generator, "", `, "default_initial_tokens": 300000`, `, "default_initial_tokens": 7`))
		test.Settings = &settings
	}
	test.Spend = map[string]int64{}
	for _, model := range models {
		if generator.IntN(2) == 0 {
			test.Spend[model] = int64(generator.IntN(400000))
		}
	}
	for index := range 1 + generator.IntN(5) {
		test.Members = append(test.Members, newRandomMember(generator, fmt.Sprintf("M%d", index), models))
	}
	return test
}

// newRandomMember draws one member and its slot usage in RoomUsage's shape.
func newRandomMember(generator *rand.Rand, name string, models []string) randomMember {
	member := randomMember{
		Name: name, Role: pick(generator, "lead", "worker", "human"), State: pick(generator, "joined", "replied", "retired", "invited"),
		Launch: pick[any](generator, "absent", nil, "running", "finished"),
	}
	slot := SlotUsage{Source: SourceEstimated}
	if generator.IntN(5) > 0 {
		slot.Model = text(pick(generator, append(models, "")...))
	}
	if generator.IntN(3) == 0 {
		budget := int64(1 + generator.IntN(300000))
		slot.TokenBudget = &budget
	}
	if generator.IntN(4) > 0 {
		slot.HasUsage = true
		slot.Source = pick(generator, SourceEstimated, SourceReported, SourcePlugin)
		slot.InputTokens, slot.OutputTokens = int64(generator.IntN(150000)), int64(generator.IntN(50000))
		slot.ConversationTokens = slot.InputTokens + slot.OutputTokens
		slot.EstimatedTokens = int64(generator.IntN(100000))
		if slot.Source != SourceEstimated {
			actual := slot.ConversationTokens
			slot.ActualTokens = &actual
		}
	}
	member.slot = slot
	member.Raw = map[string]any{
		"model": slot.Model, "token_budget": slot.TokenBudget, "input_tokens": slot.InputTokens,
		"output_tokens": slot.OutputTokens, "conversation_tokens": slot.ConversationTokens,
		"estimated_tokens": slot.EstimatedTokens, "actual_tokens": slot.ActualTokens,
		"source": slot.Source, "has_usage": slot.HasUsage,
	}
	return member
}

// goBalance runs the Go balancer on a case and encodes it like pythonBalance.
func goBalance(t *testing.T, test randomCase) string {
	t.Helper()
	var settings *Settings
	if test.Settings != nil {
		settings = mustParse(t, *test.Settings)
	}
	usages := make([]string, 0, len(test.Members))
	members := make([]BalanceMember, 0, len(test.Members))
	slots := make([]SlotUsage, 0, len(test.Members))
	for _, member := range test.Members {
		usage := BuildMemberUsage(member.slot, settings, test.Spend)
		encoded := "null"
		if usage != nil {
			encoded = usage.Value().Compact()
		}
		usages = append(usages, encoded)
		status := progress.Member{Name: member.Name, Role: member.Role, State: member.State}
		if launch, ok := member.Launch.(string); ok && launch != "absent" {
			status.LaunchState = &launch
		}
		members = append(members, BalanceMember{Member: status, Usage: usage})
		slots = append(slots, member.slot)
	}
	balance, total := "null", "null"
	if result := EvaluateBalance(members, settings); result != nil {
		balance = result.Value().Compact()
	}
	if result := SumConversationUsage(slots); result != nil {
		total = result.Value().Compact()
	}
	return "[[" + strings.Join(usages, ",") + "]," + balance + "," + total + "]"
}

// TestBalancerMatchesPython compares Go and Python results, byte for byte, on random
// teams. It runs the Python balancer through uv and skips when uv is unavailable.
func TestBalancerMatchesPython(t *testing.T) {
	pathValue, pathSet := os.LookupEnv("PATH")
	uv := paths.FindOnPath("uv", pathValue, pathSet)
	if uv == "" {
		t.Skip("uv is unavailable")
	}
	generator := rand.New(rand.NewPCG(17, 19))
	cases := make([]randomCase, 0, 2000)
	for range 2000 {
		cases = append(cases, newRandomCase(generator))
	}
	input, err := json.Marshal(cases)
	if err != nil {
		t.Fatal(err)
	}
	repository, _ := filepath.Abs("../..")
	command := exec.Command(uv, "run", "--locked", "--project", repository, "python", "-I", "-c", pythonBalance)
	command.Env = os.Environ()
	command.Stdin = strings.NewReader(string(input))
	output, err := command.Output()
	if err != nil {
		if exit, ok := err.(*exec.ExitError); ok {
			t.Fatalf("Python failed: %v\n%s", err, exit.Stderr)
		}
		t.Skipf("Python unavailable: %v", err)
	}
	var expected []json.RawMessage
	if err := json.Unmarshal(output, &expected); err != nil {
		t.Fatal(err)
	}
	unbalanced := 0
	for index, test := range cases {
		got := goBalance(t, test)
		if got != string(expected[index]) {
			t.Fatalf("case %d:\n Go     %s\n Python %s", index, got, expected[index])
		}
		if strings.Contains(got, `"balanced":false`) {
			unbalanced++
		}
	}
	if unbalanced < 50 {
		t.Errorf("only %d unbalanced cases; the generator misses the recommendation paths", unbalanced)
	}
}

// TestEvaluateBalanceRoundingBoundaryDoesNotPanic guards against 23.1 - 3.1 == 19.999999999999996
// rounding to gap 20.0 while unrounded difference fell below threshold 20.
func TestEvaluateBalanceRoundingBoundaryDoesNotPanic(t *testing.T) {
	settings := mustParse(t, `{"imbalance_gap_percent": 20, "default_initial_tokens": 1000}`)
	claude := BuildMemberUsage(reported("claude-sonnet-4-6", SourceReported, 231, 0), settings, map[string]int64{"claude-sonnet-4-6": 231})
	gemini := BuildMemberUsage(reported("gemini-2.5-pro", SourceReported, 31, 0), settings, map[string]int64{"gemini-2.5-pro": 31})
	if claude.Budget.UsedPercent != 23.1 || gemini.Budget.UsedPercent != 3.1 {
		t.Fatalf("unexpected used percentages: %v vs %v", claude.Budget.UsedPercent, gemini.Budget.UsedPercent)
	}
	balance := EvaluateBalance([]BalanceMember{
		balanceMember("Claude", "lead", "replied", claude),
		balanceMember("Gemini", "worker", "replied", gemini),
	}, settings)
	if balance == nil || balance.Balanced {
		t.Fatalf("expected unbalanced guidance at exact 20.0 gap, got %+v", balance)
	}
	if !reflect.DeepEqual(balance.Overloaded, []string{"Claude"}) || !reflect.DeepEqual(balance.Preferred, []string{"Gemini"}) {
		t.Errorf("unexpected overloaded/preferred: %+v", balance)
	}
}

func TestBudgetUsageGuardsNonPositiveInitial(t *testing.T) {
	if got := budgetUsage(0, 100, nil, "m"); got != nil {
		t.Errorf("budgetUsage(0, ...) = %+v, want nil", got)
	}
	if got := budgetUsage(-5, 100, nil, "m"); got != nil {
		t.Errorf("budgetUsage(-5, ...) = %+v, want nil", got)
	}
}
