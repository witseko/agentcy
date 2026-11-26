package shim

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/witseko/agentcy/internal/progress"
	"github.com/witseko/agentcy/internal/wire"
)

// MCP Tasks extension (SEP-2663), from src/agentcy/mcp/tasks.py. An MCP task is a
// protocol object, not a conversation task: the core keeps the fresh-run assignment and
// this file maps its state to the wire form. The MCP task ID is the only credential, so
// it is never logged or put into an error.
const (
	tasksExtension      = "io.modelcontextprotocol/tasks"
	launchTool          = "launch_participant"
	mcpTaskKey          = "mcp_task"
	taskPollIntervalMs  = 5000
	unknownMcpTask      = "Failed to retrieve task: Task not found"
	missingCapability   = -32021
	isoTimeLayout       = "2006-01-02T15:04:05Z"
	resultTypeComplete  = "complete"
	resultTypeTask      = "task"
	resultTypeInputWait = "input_required"
)

// endedMessages are the texts for launch states that end a fresh run without a cancel.
var endedMessages = map[string]string{
	"finished":  "The fresh run for %s finished. Read the conversation for its messages.",
	"failed":    "The fresh run for %s failed. Read the conversation for its events and open tasks.",
	"timed_out": "The fresh run for %s reached its deadline. Read the conversation for its events and open tasks.",
	"unknown":   "The bridge cannot confirm the state of the fresh run for %s. Check the provider before you launch a replacement.",
}

// failedEndings are the endings that the host should report as a failed tool call.
var failedEndings = map[string]bool{"failed": true, "timed_out": true, "unknown": true}

// TaskParams are the parameters of tasks/get, tasks/cancel and tasks/update. The
// inputResponses of tasks/update are ignored: the bridge never asks a task for input.
type TaskParams struct {
	mcp.ParamsBase
	TaskID         string          `json:"taskId"`
	InputResponses json.RawMessage `json:"inputResponses,omitempty"`
}

// wantsMcpTask tells whether a tool call may get an MCP task instead of its normal
// result: only a 2026-07-28 request that declares the extension in its own capabilities.
func wantsMcpTask(request *mcp.CallToolRequest) bool {
	return isModern(request) && metaDeclaresExtension(request.Params.GetMeta(), tasksExtension)
}

// metaDeclaresExtension tells whether the per-request client capabilities in _meta name
// an extension.
func metaDeclaresExtension(meta map[string]any, extension string) bool {
	capabilities, _ := meta[mcp.MetaKeyClientCapabilities].(map[string]any)
	extensions, _ := capabilities["extensions"].(map[string]any)
	_, declared := extensions[extension]
	return declared
}

// isoTime formats Unix seconds as an ISO 8601 UTC timestamp.
func isoTime(seconds int64) string {
	return time.Unix(seconds, 0).UTC().Format(isoTimeLayout)
}

// wireTask maps the core's view of a fresh run to the fields of an MCP task. A run that
// ends by failure, timeout, or an uncertain state is a completed MCP task whose result has
// isError; the spec reserves the failed status for JSON-RPC errors.
func wireTask(taskID string, view wire.Value) (wire.Value, error) {
	launchState := textField(view, "launch_state")
	name := textField(view, "participant")
	created, _ := view.Get("created")
	updated, _ := view.Get("updated")
	createdSeconds, _ := created.IntOf()
	updatedSeconds, _ := updated.IntOf()
	task := wire.NewObject()
	task.Set("taskId", wire.TextValue(taskID))
	task.Set("createdAt", wire.TextValue(isoTime(createdSeconds)))
	task.Set("lastUpdatedAt", wire.TextValue(isoTime(updatedSeconds)))
	task.Set("ttlMs", wire.NullValue())
	task.Set("pollIntervalMs", wire.IntValue(taskPollIntervalMs))
	switch {
	case progress.IsPendingState(launchState):
		task.Set("status", wire.TextValue("working"))
		task.Set("statusMessage", wire.TextValue(fmt.Sprintf("The fresh run for %s is %s.", name, launchState)))
	case launchState == "cancelled":
		task.Set("status", wire.TextValue("cancelled"))
		task.Set("statusMessage", wire.TextValue(fmt.Sprintf("The fresh run for %s was cancelled.", name)))
	case endedMessages[launchState] != "":
		message := fmt.Sprintf(endedMessages[launchState], name)
		task.Set("status", wire.TextValue("completed"))
		task.Set("statusMessage", wire.TextValue(message))
		task.Set("result", taskToolResult(name, launchState, message, failedEndings[launchState]))
	default:
		return wire.Value{}, &jsonrpc.Error{Code: jsonrpc.CodeInternalError, Message: "Unmapped launch state " + pythonRepr(launchState)}
	}
	return task, nil
}

// taskToolResult builds the CallToolResult that a finished MCP task returns. Its text is
// the summary as Python's json.dumps writes it.
func taskToolResult(name, launchState, message string, isError bool) wire.Value {
	summary := wire.NewObject()
	summary.Set("participant", wire.TextValue(name))
	summary.Set("launch_state", wire.TextValue(launchState))
	summary.Set("next", wire.TextValue(message))
	content := wire.NewObject()
	content.Set("type", wire.TextValue("text"))
	content.Set("text", wire.TextValue(pythonDumps(summary)))
	result := wire.NewObject()
	result.Set("content", wire.ListValue([]wire.Value{content}))
	result.Set("structuredContent", summary)
	result.Set("isError", wire.BoolValue(isError))
	return result
}

// taskResultFromValue replaces a launch_participant result that carries a new MCP task
// with that task, as McpTasksExtension.intercept_tool_call does. ok is false for any
// other result.
func taskResultFromValue(name string, launched wire.Value) (wire.Value, bool, error) {
	if name != launchTool {
		return wire.Value{}, false, nil
	}
	created, found := launched.Get(mcpTaskKey)
	if !found {
		return wire.Value{}, false, nil
	}
	task, err := wireTask(textField(created, "task_id"), created)
	if err != nil {
		return wire.Value{}, false, err
	}
	shaped := wire.NewObject()
	shaped.Set("resultType", wire.TextValue(resultTypeTask))
	for _, key := range task.Keys() {
		value, _ := task.Get(key)
		shaped.Set(key, value)
	}
	return shaped, true, nil
}

// addTaskMethods binds tasks/get, tasks/cancel and tasks/update.
func (shim *shimServer) addTaskMethods(server *mcp.Server) error {
	return errors.Join(
		mcp.AddReceivingCustomMethod(server, "tasks/get", func(ctx context.Context, session *mcp.ServerSession, params *TaskParams) (*orderedResult, error) {
			return shim.taskMethod(ctx, session, params, "tasks/get")
		}),
		mcp.AddReceivingCustomMethod(server, "tasks/cancel", func(ctx context.Context, session *mcp.ServerSession, params *TaskParams) (*orderedResult, error) {
			return shim.taskMethod(ctx, session, params, "tasks/cancel")
		}),
		mcp.AddReceivingCustomMethod(server, "tasks/update", func(ctx context.Context, session *mcp.ServerSession, params *TaskParams) (*orderedResult, error) {
			return shim.taskMethod(ctx, session, params, "tasks/update")
		}),
	)
}

// taskMethod serves one tasks/* request. The methods exist only in the 2026-07-28 era,
// and each request must declare the extension.
func (shim *shimServer) taskMethod(ctx context.Context, session *mcp.ServerSession, params *TaskParams, method string) (*orderedResult, error) {
	if params == nil {
		params = &TaskParams{}
	}
	meta := params.GetMeta()
	if !isModernEra(sessionEra(session, meta)) {
		return nil, &jsonrpc.Error{Code: jsonrpc.CodeMethodNotFound, Message: "Method not found", Data: json.RawMessage(strconv.Quote(method))}
	}
	if !metaDeclaresExtension(meta, tasksExtension) {
		return nil, missingExtensionError(tasksExtension)
	}
	if method == "tasks/cancel" {
		if _, err := shim.callTaskCore(ctx, &wire.McpTaskCancel{Workspace: shim.workspace, TaskID: params.TaskID}); err != nil {
			return nil, err
		}
		return shim.newResult(completeBody(wire.NewObject())), nil
	}
	view, err := shim.callTaskCore(ctx, &wire.McpTaskStatus{Workspace: shim.workspace, TaskID: params.TaskID})
	if err != nil {
		return nil, err
	}
	if method == "tasks/update" {
		return shim.newResult(completeBody(wire.NewObject())), nil
	}
	task, err := wireTask(params.TaskID, view)
	if err != nil {
		return nil, err
	}
	return shim.newResult(completeBody(task)), nil
}

// completeBody appends the 2026-07-28 resultType to a result.
func completeBody(body wire.Value) wire.Value {
	body.Set("resultType", wire.TextValue(resultTypeComplete))
	return body
}

// callTaskCore sends one MCP task request and translates its rejections: an ID that no
// run could have, and the core's unknown-task answer, both become the one unknown-task
// error.
func (shim *shimServer) callTaskCore(ctx context.Context, request wire.Request) (wire.Value, error) {
	unknown := &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: unknownMcpTask}
	if wire.Validate(request) != nil {
		return wire.Value{}, unknown
	}
	view, err := shim.core.Exchange(ctx, request)
	var rejection *wire.CoreError
	switch {
	case err == nil:
		return view, nil
	case errors.As(err, &rejection) && rejection.Message == unknownMcpTask:
		return wire.Value{}, unknown
	default:
		return wire.Value{}, &jsonrpc.Error{Code: jsonrpc.CodeInternalError, Message: err.Error()}
	}
}

// missingExtensionError is the SEP-2133 error for a request without a required extension.
func missingExtensionError(extension string) error {
	data := wire.NewObject()
	required := wire.NewObject()
	extensions := wire.NewObject()
	extensions.Set(extension, wire.NewObject())
	required.Set("extensions", extensions)
	data.Set("requiredCapabilities", required)
	return &jsonrpc.Error{
		Code:    missingCapability,
		Message: "Client did not declare required extension " + pythonRepr(extension),
		Data:    json.RawMessage(data.Compact()),
	}
}

// sessionEra returns the protocol version of one custom-method request: the per-request
// _meta value in the 2026-07-28 era, else the version negotiated by initialize.
func sessionEra(session *mcp.ServerSession, meta map[string]any) string {
	if era, ok := meta[mcp.MetaKeyProtocolVersion].(string); ok && era != "" {
		return era
	}
	if session != nil {
		if params := session.InitializeParams(); params != nil {
			return params.ProtocolVersion
		}
	}
	return ""
}
