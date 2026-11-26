package fresh

import (
	"bytes"
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/witseko/agentcy/internal/wire"
)

// readProtocolFile reads a file of the checked-in protocol directory.
func readProtocolFile(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "protocol", name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// schemaDefinition is the part of a JSON schema definition that the test compares.
type schemaDefinition struct {
	Properties map[string]struct {
		Enum []string `json:"enum"`
	} `json:"properties"`
	Required []string `json:"required"`
}

// loadSchema reads the $defs of the fresh-run contract schema.
func loadSchema(t *testing.T) map[string]schemaDefinition {
	t.Helper()
	var schema struct {
		Defs map[string]schemaDefinition `json:"$defs"`
	}
	if err := json.Unmarshal(readProtocolFile(t, "fresh-runs.v1.schema.json"), &schema); err != nil {
		t.Fatal(err)
	}
	return schema.Defs
}

// jsonFields returns the JSON names of a struct type and the ones that are required
// (not omitempty), sorted.
func jsonFields(value any) (all []string, required []string) {
	kind := reflect.TypeOf(value)
	for index := 0; index < kind.NumField(); index++ {
		tag := kind.Field(index).Tag.Get("json")
		name, options, _ := strings.Cut(tag, ",")
		all = append(all, name)
		if options != "omitempty" {
			required = append(required, name)
		}
	}
	sort.Strings(all)
	sort.Strings(required)
	return all, required
}

func TestRequestStructsMatchTheSchema(t *testing.T) {
	definitions := loadSchema(t)
	for definition, value := range map[string]any{
		"start_arguments":   StartArguments{},
		"inspect_arguments": assignmentReference{},
		"cancel_arguments":  assignmentReference{},
	} {
		all, required := jsonFields(value)
		wanted := definitions[definition]
		sortedRequired := append([]string(nil), wanted.Required...)
		sort.Strings(sortedRequired)
		if !reflect.DeepEqual(all, slices.Sorted(maps.Keys(wanted.Properties))) || !reflect.DeepEqual(required, sortedRequired) {
			t.Errorf("%s: fields %v required %v, schema %v required %v", definition, all, required, slices.Sorted(maps.Keys(wanted.Properties)), sortedRequired)
		}
	}
}

func TestResultParsersMatchTheSchema(t *testing.T) {
	definitions := loadSchema(t)
	states := definitions["run_result"].Properties["state"].Enum
	for _, state := range states {
		if !isRunState(state) {
			t.Errorf("schema state %q is refused", state)
		}
	}
	if isRunState(StateQueued) || len(states) != 7 {
		t.Errorf("states %v", states)
	}
	full := `{"state": "finished", "exit_code": 0, "input_tokens": 1, "output_tokens": 2}`
	if _, err := parseRunResult(mustParse(t, full)); err != nil {
		t.Errorf("every schema property: %v", err)
	}
	if names := slices.Sorted(maps.Keys(definitions["run_result"].Properties)); strings.Join(names, ",") != "exit_code,input_tokens,output_tokens,state" {
		t.Errorf("run_result properties %v", names)
	}
	if names := slices.Sorted(maps.Keys(definitions["capabilities_result"].Properties)); strings.Join(names, ",") != "cost_cap,token_cap,version" {
		t.Errorf("capabilities_result properties %v", names)
	}
}

func TestContractExamplesValidate(t *testing.T) {
	var examples []struct {
		Name              string          `json:"name"`
		Arguments         json.RawMessage `json:"arguments"`
		StructuredContent json.RawMessage `json:"structuredContent"`
	}
	if err := json.Unmarshal(readProtocolFile(t, "fresh-runs.v1.examples.json"), &examples); err != nil {
		t.Fatal(err)
	}
	if len(examples) != 4 {
		t.Fatalf("%d examples", len(examples))
	}
	for _, example := range examples {
		content := mustParse(t, string(example.StructuredContent))
		var err error
		switch example.Name {
		case ToolCapabilities:
			_, err = parseCapabilities(content)
		case ToolStart:
			roundTrip(t, example.Arguments, &StartArguments{})
			_, err = parseRunResult(content)
		default:
			roundTrip(t, example.Arguments, &assignmentReference{})
			_, err = parseRunResult(content)
		}
		if err != nil {
			t.Errorf("%s: %v", example.Name, err)
		}
	}
}

// roundTrip decodes example arguments strictly into target and checks that encoding
// target gives the same JSON.
func roundTrip(t *testing.T, arguments json.RawMessage, target any) {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(arguments))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		t.Fatalf("decode %s: %v", arguments, err)
	}
	encoded, _ := json.Marshal(target)
	var want, got any
	_ = json.Unmarshal(arguments, &want)
	_ = json.Unmarshal(encoded, &got)
	if !reflect.DeepEqual(want, got) {
		t.Errorf("encoded %s, want %s", encoded, arguments)
	}
}

func mustParse(t *testing.T, text string) wire.Value {
	t.Helper()
	value, err := wire.Parse([]byte(text))
	if err != nil {
		t.Fatalf("parse %s: %v", text, err)
	}
	return value
}

func TestParseRunResultRefusesWhatPydanticRefuses(t *testing.T) {
	for _, text := range []string{
		`{}`, `[]`, `"running"`, `{"state": "queued"}`, `{"state": "done"}`, `{"state": 1}`,
		`{"state": "running", "extra": 1}`, `{"state": "running", "exit_code": 1.5}`,
		`{"state": "running", "input_tokens": -1}`, `{"state": "running", "output_tokens": 1000000001}`,
		`{"state": "running", "exit_code": "x"}`,
	} {
		if _, err := parseRunResult(mustParse(t, text)); err == nil {
			t.Errorf("%s accepted", text)
		}
	}
	result, err := parseRunResult(mustParse(t, `{"state": "failed", "exit_code": 2, "input_tokens": null}`))
	if err != nil || result.State != StateFailed || *result.ExitCode != 2 || result.InputTokens != nil {
		t.Errorf("result %+v, %v", result, err)
	}
}

func TestParseCapabilitiesNeedsVersionOne(t *testing.T) {
	for _, text := range []string{`{}`, `{"version": 2}`, `{"version": true}`, `{"version": "1"}`, `{"version": 1, "token_cap": 1}`, `{"version": 1, "extra": false}`} {
		if _, err := parseCapabilities(mustParse(t, text)); err == nil {
			t.Errorf("%s accepted", text)
		}
	}
	capabilities, err := parseCapabilities(mustParse(t, `{"version": 1, "token_cap": true}`))
	if err != nil || !capabilities.TokenCap || capabilities.CostCap {
		t.Errorf("capabilities %+v, %v", capabilities, err)
	}
}

func TestStopReason(t *testing.T) {
	now := mustTime(1000)
	open := Run{Open: true, Deadline: 2000}
	cases := []struct {
		name string
		run  Run
		want string
	}{
		{"running", open, ""},
		{"closed", Run{Deadline: 2000}, StateCancelled},
		{"retired", Run{Open: true, Retired: true, Deadline: 2000}, StateCancelled},
		{"cancel requested", Run{Open: true, CancelRequested: true, Deadline: 2000}, StateCancelled},
		{"round moved on", Run{Open: true, Round: "sealed", CurrentRound: "critique", Deadline: 2000}, StateCancelled},
		{"same round", Run{Open: true, Round: "sealed", CurrentRound: "sealed", RoundDeadline: 1500, Deadline: 2000}, ""},
		{"round deadline", Run{Open: true, Round: "sealed", CurrentRound: "sealed", RoundDeadline: 1000, Deadline: 2000}, StateCancelled},
		{"deadline", Run{Open: true, Deadline: 1000}, StateTimedOut},
		{"closed after deadline", Run{Deadline: 1000}, StateCancelled},
	}
	for _, test := range cases {
		if got := StopReason(test.run, now); got != test.want {
			t.Errorf("%s: %q, want %q", test.name, got, test.want)
		}
	}
}
