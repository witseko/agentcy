package tokens

import (
	"reflect"
	"testing"

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
