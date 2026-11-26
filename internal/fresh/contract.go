package fresh

import (
	"errors"

	"github.com/witseko/agentcy/internal/wire"
)

// This file is version 1 of the fresh-run MCP contract (plugin_contract.py and
// protocol/fresh-runs.v1.schema.json): the arguments the daemon sends and the
// structured results it accepts.

// The plugin tools of contract version 1.
const (
	ToolCapabilities = "capabilities"
	ToolStart        = "start"
	ToolInspect      = "inspect"
	ToolCancel       = "cancel"
)

// The run states a plugin reports (RunState in plugin_contract.py). The first three are
// also the pending states of an assignment.
const (
	StateQueued    = "queued"
	StateStarting  = "starting"
	StateRunning   = "running"
	StateFinished  = "finished"
	StateFailed    = "failed"
	StateCancelled = "cancelled"
	StateTimedOut  = "timed_out"
	StateUnknown   = "unknown"
)

// maxTokenUsage is the largest token count a plugin may report.
const maxTokenUsage = 1_000_000_000

// errContract is the error of a result that breaks the contract. Like pydantic with
// hide_input_in_errors, it never repeats the plugin's reply.
var errContract = errors.New("Fresh-run plugin reply breaks the contract")

// Capabilities is the reply of the capabilities tool: the contract version and the hard
// limits the plugin enforces.
type Capabilities struct {
	Version  int64
	TokenCap bool
	CostCap  bool
}

// StartArguments launches a worker bound to a local read-only workspace and bridge. A
// nil cap is left out of the request, as model_dump(exclude_none=True) does.
type StartArguments struct {
	AssignmentID    string `json:"assignment_id"`
	Workspace       string `json:"workspace"`
	BridgeHome      string `json:"bridge_home"`
	JoinCode        string `json:"join_code"`
	Model           string `json:"model"`
	TimeoutSeconds  int64  `json:"timeout_seconds"`
	TokenCap        *int64 `json:"token_cap,omitempty"`
	CostCapMicroUSD *int64 `json:"cost_cap_microusd,omitempty"`
}

// assignmentReference names an assignment in an inspect or cancel call.
type assignmentReference struct {
	AssignmentID string `json:"assignment_id"`
}

// RunResult is the reply of start, inspect and cancel: a provider state without native
// identifiers or content. A nil field was absent or null.
type RunResult struct {
	State        string
	ExitCode     *int64
	InputTokens  *int64
	OutputTokens *int64
}

// parseCapabilities validates a capabilities reply: an object with version 1 and the
// optional booleans token_cap and cost_cap, and nothing else.
func parseCapabilities(value wire.Value) (Capabilities, error) {
	if !onlyKnownFields(value, "version", "token_cap", "cost_cap") {
		return Capabilities{}, errContract
	}
	version, found := value.Get("version")
	if !found {
		return Capabilities{}, errContract
	}
	number, valid := laxInteger(version, 1, 1)
	if !valid || version.Kind != wire.Number {
		return Capabilities{}, errContract
	}
	result := Capabilities{Version: number}
	if result.TokenCap, valid = optionalBool(value, "token_cap"); !valid {
		return Capabilities{}, errContract
	}
	if result.CostCap, valid = optionalBool(value, "cost_cap"); !valid {
		return Capabilities{}, errContract
	}
	return result, nil
}

// optionalBool reads a boolean member that defaults to false.
func optionalBool(object wire.Value, key string) (bool, bool) {
	member, found := object.Get(key)
	if !found {
		return false, true
	}
	return member.Boolean, member.Kind == wire.Bool
}

// parseRunResult validates a run result: a known state, an optional integer exit code
// and optional token counts from 0 to 1,000,000,000, and nothing else.
func parseRunResult(value wire.Value) (RunResult, error) {
	if !onlyKnownFields(value, "state", "exit_code", "input_tokens", "output_tokens") {
		return RunResult{}, errContract
	}
	state, found := value.Get("state")
	if !found || state.Kind != wire.String || !isRunState(state.Text) {
		return RunResult{}, errContract
	}
	result := RunResult{State: state.Text}
	valid := true
	if result.ExitCode, valid = optionalInteger(value, "exit_code", -1<<53, 1<<53); !valid {
		return RunResult{}, errContract
	}
	if result.InputTokens, valid = optionalInteger(value, "input_tokens", 0, maxTokenUsage); !valid {
		return RunResult{}, errContract
	}
	if result.OutputTokens, valid = optionalInteger(value, "output_tokens", 0, maxTokenUsage); !valid {
		return RunResult{}, errContract
	}
	return result, nil
}

// optionalInteger reads an integer member that may be absent or null.
func optionalInteger(object wire.Value, key string, minimum, maximum int64) (*int64, bool) {
	member, found := object.Get(key)
	if !found || member.Kind == wire.Null {
		return nil, true
	}
	number, valid := laxInteger(member, minimum, maximum)
	if !valid {
		return nil, false
	}
	return &number, true
}

// isRunState reports a state a plugin may report. A plugin never reports queued.
func isRunState(state string) bool {
	switch state {
	case StateStarting, StateRunning, StateFinished, StateFailed, StateCancelled, StateTimedOut, StateUnknown:
		return true
	}
	return false
}

// IsPendingState reports a state in which the bridge still tracks the run
// (PENDING_STATES in assignment_tables.py).
func IsPendingState(state string) bool {
	return state == StateQueued || state == StateStarting || state == StateRunning
}
