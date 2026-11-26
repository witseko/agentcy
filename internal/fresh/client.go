package fresh

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"time"

	"github.com/witseko/agentcy/internal/wire"
)

// CallTimeout bounds one plugin exchange, from process start to exit (RPC_TIMEOUT in
// plugin_client.py).
const CallTimeout = 5 * time.Second

// handshakeVersion is the protocol version the client asks for in initialize, the one
// the Python SDK client sends (LATEST_HANDSHAKE_VERSION in mcp_types/version.py).
const handshakeVersion = "2025-11-25"

// handshakeVersions are the protocol versions a plugin may answer initialize with
// (HANDSHAKE_PROTOCOL_VERSIONS in mcp_types/version.py).
var handshakeVersions = map[string]bool{
	"2024-11-05": true, "2025-03-26": true, "2025-06-18": true, "2025-11-25": true,
}

// The errors of a plugin call. None repeats anything the plugin wrote.
var (
	errOperation          = errors.New("Fresh-run plugin operation failed")
	errContractMissing    = errors.New("Fresh-run plugin contract is unavailable")
	errCapUnsupported     = errors.New("Fresh-run plugin cannot enforce the configured cap")
	errStateNotConfirmed  = errors.New("Fresh-run plugin state could not be confirmed")
	errUnexpectedResponse = errors.New("Fresh-run plugin sent an unexpected response")
)

// Provider runs the plugin tools for the worker. Client implements it; tests replace it.
type Provider interface {
	// CheckCapabilities checks that the plugin speaks contract version 1 and enforces
	// the caps configured for it.
	CheckCapabilities(ctx context.Context, plugin Plugin) error
	// Start sends start for one assignment.
	Start(ctx context.Context, plugin Plugin, arguments StartArguments) (RunResult, error)
	// Operate sends inspect or cancel for one assignment.
	Operate(ctx context.Context, plugin Plugin, tool, assignmentID string) (RunResult, error)
}

// Client runs each plugin tool in a new plugin process over MCP stdio: newline-delimited
// JSON-RPC with initialize, notifications/initialized and one tools/call, then the
// process ends. Port of plugin_client.py without the MCP SDK.
type Client struct {
	// DataDir is the bridge data directory, passed as AGENTCY_HOME.
	DataDir string
	// Binary is the absolute path of the agentcy executable, passed as AGENTCY_BIN, or
	// "" to pass nothing.
	Binary string
	// LookupEnv reads the daemon's environment; os.LookupEnv when nil.
	LookupEnv func(name string) (string, bool)
	// Timeout replaces CallTimeout, for tests.
	Timeout time.Duration
}

// The client must satisfy the worker's interface.
var _ Provider = (*Client)(nil)

// CheckCapabilities checks the version and the caps (check_capabilities).
func (client *Client) CheckCapabilities(ctx context.Context, plugin Plugin) error {
	reply, err := client.call(ctx, plugin, ToolCapabilities, map[string]any{})
	if err != nil {
		return errContractMissing
	}
	capabilities, err := parseCapabilities(reply)
	if err != nil {
		return errContractMissing
	}
	if (plugin.TokenCap != nil && !capabilities.TokenCap) || (plugin.CostCapMicroUSD != nil && !capabilities.CostCap) {
		return errCapUnsupported
	}
	return nil
}

// Start sends start and parses the state (run_operation with a Start).
func (client *Client) Start(ctx context.Context, plugin Plugin, arguments StartArguments) (RunResult, error) {
	return client.runOperation(ctx, plugin, ToolStart, arguments)
}

// Operate sends inspect or cancel and parses the state (run_operation with an ID).
func (client *Client) Operate(ctx context.Context, plugin Plugin, tool, assignmentID string) (RunResult, error) {
	return client.runOperation(ctx, plugin, tool, assignmentReference{AssignmentID: assignmentID})
}

// runOperation calls a tool that answers with a run result.
func (client *Client) runOperation(ctx context.Context, plugin Plugin, tool string, arguments any) (RunResult, error) {
	reply, err := client.call(ctx, plugin, tool, arguments)
	if err != nil {
		return RunResult{}, errStateNotConfirmed
	}
	result, err := parseRunResult(reply)
	if err != nil {
		return RunResult{}, errStateNotConfirmed
	}
	return result, nil
}

// call runs one tool in a new plugin process within the timeout and returns its
// structuredContent.
func (client *Client) call(ctx context.Context, plugin Plugin, tool string, arguments any) (wire.Value, error) {
	timeout := client.Timeout
	if timeout <= 0 {
		timeout = CallTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	lookupEnv := client.LookupEnv
	if lookupEnv == nil {
		lookupEnv = os.LookupEnv
	}
	environment := pluginEnvironment(plugin, spawnSettings{dataDir: client.DataDir, binary: client.Binary, lookupEnv: lookupEnv})
	process, err := startPlugin(plugin, environment)
	if err != nil {
		return wire.Value{}, err
	}
	reply, err := exchange(ctx, process, tool, arguments)
	deadline, _ := ctx.Deadline()
	if err != nil {
		deadline = time.Now()
	}
	// As in the Python client, the shutdown counts toward the timeout.
	if !process.stop(deadline) && err == nil && ctx.Err() != nil {
		err = errCommunication
	}
	return reply, err
}

// exchange performs the handshake and one tools/call on a started process.
func exchange(ctx context.Context, process *pluginProcess, tool string, arguments any) (wire.Value, error) {
	initialize := map[string]any{
		"protocolVersion": handshakeVersion,
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "agentcy", "version": "0.1.0"},
	}
	if err := sendRequest(process, 1, "initialize", initialize); err != nil {
		return wire.Value{}, err
	}
	result, err := awaitResult(ctx, process, "1")
	if err != nil {
		return wire.Value{}, err
	}
	version, _ := result.Get("protocolVersion")
	if version.Kind != wire.String || !handshakeVersions[version.Text] {
		return wire.Value{}, errUnexpectedResponse
	}
	if err := sendMessage(process, map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"}); err != nil {
		return wire.Value{}, err
	}
	call := map[string]any{"name": tool, "arguments": arguments}
	if err := sendRequest(process, 2, "tools/call", call); err != nil {
		return wire.Value{}, err
	}
	result, err = awaitResult(ctx, process, "2")
	if err != nil {
		return wire.Value{}, err
	}
	return structuredContent(result)
}

// structuredContent returns the structured result of a tools/call result, or
// errOperation for a tool error or a result without one.
func structuredContent(result wire.Value) (wire.Value, error) {
	if isError, _ := result.Get("isError"); isError.Truthy() {
		return wire.Value{}, errOperation
	}
	content, found := result.Get("structuredContent")
	if !found || content.Kind == wire.Null {
		return wire.Value{}, errOperation
	}
	return content, nil
}

// sendRequest writes a JSON-RPC request with a numeric ID.
func sendRequest(process *pluginProcess, id int, method string, params any) error {
	return sendMessage(process, map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
}

// sendMessage writes one JSON-RPC message.
func sendMessage(process *pluginProcess, message map[string]any) error {
	encoded, err := json.Marshal(message)
	if err != nil {
		return errCommunication
	}
	return process.write(encoded)
}

// awaitResult reads messages until the response with the ID arrives and returns its
// result object. Notifications and other messages before it are skipped. Output that is
// not JSON, an error response, the end of the output or the end of ctx is an error.
func awaitResult(ctx context.Context, process *pluginProcess, id string) (wire.Value, error) {
	for {
		var line outputLine
		select {
		case <-ctx.Done():
			return wire.Value{}, errCommunication
		case line = <-process.lines:
		}
		if line.err != nil {
			return wire.Value{}, errCommunication
		}
		message, err := wire.Parse(line.text)
		if err != nil || message.Kind != wire.Object {
			return wire.Value{}, errCommunication
		}
		messageID, hasID := message.Get("id")
		if !hasID || messageID.Kind != wire.Number || messageID.Literal != id || message.Has("method") {
			continue
		}
		result, found := message.Get("result")
		if !found || result.Kind != wire.Object {
			return wire.Value{}, errOperation
		}
		return result, nil
	}
}
