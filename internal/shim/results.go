package shim

import (
	"context"
	"encoding/json"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/witseko/agentcy/internal/wire"
)

// The capabilities the Python shim advertises. The handshake era advertises no
// extensions (Python still serves skills/* there), and the 2026-07-28 era reports the
// SDK's list-changed defaults.
const (
	legacyCapabilities = `{"prompts":{"listChanged":false},"resources":{"listChanged":false,"subscribe":false},"tools":{"listChanged":false}}`
	modernCapabilities = `{"prompts":{"listChanged":true},"resources":{"listChanged":true,"subscribe":true},"tools":{"listChanged":true}}`
)

// orderedResult is a JSON-RPC result whose members keep the order the Python shim emits.
// The SDK sets _meta (the server identity) on 2026-07-28 results; it is written last.
type orderedResult struct {
	mcp.ResultBase
	body       wire.Value
	serverInfo wire.Value
}

// MarshalJSON writes the body and, when the SDK set any metadata, the server identity.
func (result *orderedResult) MarshalJSON() ([]byte, error) {
	body := result.body.Copy()
	if len(result.Meta) > 0 {
		meta := wire.NewObject()
		meta.Set(mcp.MetaKeyServerInfo, result.serverInfo)
		body.Set("_meta", meta)
	}
	return []byte(body.Compact()), nil
}

// newResult wraps a body in an orderedResult of this server.
func (shim *shimServer) newResult(body wire.Value) *orderedResult {
	return &orderedResult{body: body, serverInfo: shim.serverInfo}
}

// isModern reports whether a request uses the 2026-07-28 protocol era.
func isModern(request mcp.Request) bool {
	versioned, ok := request.(interface{ ProtocolVersion() string })
	return ok && versioned.ProtocolVersion() >= modernEra
}

// shapeResults is a receiving middleware that gives every result the shape and member
// order of the Python shim, for both protocol eras. It serves the lists itself, checks a
// tools/call's requestState before the tool runs, and reshapes tool results.
func (shim *shimServer) shapeResults(next mcp.MethodHandler) mcp.MethodHandler {
	return func(ctx context.Context, method string, request mcp.Request) (mcp.Result, error) {
		// Only standard methods ask for the era: the SDK's era lookup dereferences the
		// params of a custom method that arrives without params.
		switch method {
		case "initialize":
			return shim.initializeResult(next(ctx, method, request))
		case "server/discover":
			if _, err := next(ctx, method, request); err != nil {
				return nil, err
			}
			return shim.discoverResult(), nil
		case "tools/list":
			return shim.newResult(cacheableList("tools", shim.toolList(isModern(request)), isModern(request))), nil
		case "tools/call":
			return shim.callTool(ctx, method, request, next)
		case "resources/list":
			return shim.newResult(cacheableList("resources", shim.resourceList(), isModern(request))), nil
		case "resources/templates/list":
			return shim.newResult(cacheableList("resourceTemplates", wire.ListValue(nil), isModern(request))), nil
		case "prompts/list":
			return shim.newResult(cacheableList("prompts", wire.ListValue(nil), isModern(request))), nil
		case "resources/read":
			return shim.readResource(request, isModern(request))
		}
		return next(ctx, method, request)
	}
}

// cacheableList builds a list result; in the 2026-07-28 era it adds the private,
// immediately stale cache hints and the resultType, with the keys sorted as Python sends.
func cacheableList(key string, items wire.Value, modern bool) wire.Value {
	body := wire.NewObject()
	body.Set(key, items)
	if !modern {
		return body
	}
	body.Set("cacheScope", wire.TextValue("private"))
	body.Set("resultType", wire.TextValue(resultTypeComplete))
	body.Set("ttlMs", wire.IntValue(0))
	return sortedObject(body, func(value wire.Value) wire.Value { return value })
}

// toolList returns the golden tool entries of the era in declaration order.
func (shim *shimServer) toolList(modern bool) wire.Value {
	if modern {
		return wire.ListValue(shim.toolEntries)
	}
	return wire.ListValue(shim.legacyToolEntries)
}

// initializeResult reshapes the SDK's initialize result into the Python form.
func (shim *shimServer) initializeResult(result mcp.Result, err error) (mcp.Result, error) {
	if err != nil {
		return nil, err
	}
	initialized, ok := result.(*mcp.InitializeResult)
	if !ok {
		return result, nil
	}
	capabilities, _ := wire.Parse([]byte(legacyCapabilities))
	body := wire.NewObject()
	body.Set("capabilities", capabilities)
	if shim.instructions != "" {
		body.Set("instructions", wire.TextValue(shim.instructions))
	}
	body.Set("protocolVersion", wire.TextValue(initialized.ProtocolVersion))
	body.Set("serverInfo", sortedObject(shim.serverInfo, func(value wire.Value) wire.Value {
		if value.Kind != wire.Array {
			return value
		}
		icons := make([]wire.Value, len(value.Items))
		for index, icon := range value.Items {
			icons[index] = sortedObject(icon, func(member wire.Value) wire.Value { return member })
		}
		return wire.ListValue(icons)
	}))
	return &orderedResult{body: body}, nil
}

// discoverResult returns the 2026-07-28 server/discover result in the Python form.
func (shim *shimServer) discoverResult() *orderedResult {
	capabilities, _ := wire.Parse([]byte(modernCapabilities))
	if shim.withExtensions {
		extensions := wire.NewObject()
		extensions.Set(skillsExtension, wire.NewObject())
		extensions.Set(tasksExtension, wire.NewObject())
		withExtensions := wire.NewObject()
		withExtensions.Set("extensions", extensions)
		for _, key := range capabilities.Keys() {
			value, _ := capabilities.Get(key)
			withExtensions.Set(key, value)
		}
		capabilities = withExtensions
	}
	body := wire.NewObject()
	body.Set("cacheScope", wire.TextValue("private"))
	body.Set("capabilities", capabilities)
	if shim.instructions != "" {
		body.Set("instructions", wire.TextValue(shim.instructions))
	}
	body.Set("resultType", wire.TextValue(resultTypeComplete))
	body.Set("supportedVersions", wire.TextList([]string{modernEra}))
	body.Set("ttlMs", wire.IntValue(0))
	return shim.newResult(body)
}

// callTool runs a tools/call and reshapes its result: an unknown tool and a tool error
// become error results, an input request becomes an input_required result, and a new
// MCP task replaces the launch result.
func (shim *shimServer) callTool(ctx context.Context, method string, request mcp.Request, next mcp.MethodHandler) (mcp.Result, error) {
	call, ok := request.(*mcp.CallToolRequest)
	if !ok {
		return next(ctx, method, request)
	}
	modern := isModern(request)
	params := call.Params
	if _, known := shim.tools[params.Name]; !known {
		return shim.newResult(toolResultBody("Unknown tool: "+params.Name, true, modern)), nil
	}
	if params.RequestState != "" {
		if _, err := shim.signer.verify(params.RequestState, params.Name, argumentsValue(params.Arguments)); err != nil {
			return nil, invalidRequestStateError()
		}
	}
	result, err := next(ctx, method, request)
	if err != nil {
		return nil, err
	}
	toolResult, ok := result.(*mcp.CallToolResult)
	if !ok {
		return result, nil
	}
	if toolResult.InputRequests != nil {
		return shim.newResult(inputRequiredBody(toolResult)), nil
	}
	task, isTask, err := taskResultFor(params.Name, toolResult)
	if err != nil {
		return nil, err
	}
	if isTask {
		return shim.newResult(task), nil
	}
	text := ""
	if len(toolResult.Content) == 1 {
		if content, isText := toolResult.Content[0].(*mcp.TextContent); isText {
			text = content.Text
		}
	}
	return shim.newResult(toolResultBody(text, toolResult.IsError, modern)), nil
}

// invalidRequestStateError is the frozen error for a requestState that fails
// verification; the reason never reaches the client.
func invalidRequestStateError() error {
	return &jsonrpc.Error{
		Code:    jsonrpc.CodeInvalidParams,
		Message: "Invalid or expired requestState",
		Data:    json.RawMessage(`{"reason":"invalid_request_state"}`),
	}
}

// argumentsValue parses tool arguments; anything but an object counts as no arguments.
func argumentsValue(raw json.RawMessage) wire.Value {
	arguments, err := wire.Parse(raw)
	if err != nil || arguments.Kind != wire.Object {
		return wire.NewObject()
	}
	return arguments
}

// toolResultBody builds a tool result with one text block.
func toolResultBody(text string, isError, modern bool) wire.Value {
	content := wire.NewObject()
	content.Set("text", wire.TextValue(text))
	content.Set("type", wire.TextValue("text"))
	body := wire.NewObject()
	body.Set("content", wire.ListValue([]wire.Value{content}))
	body.Set("isError", wire.BoolValue(isError))
	if modern {
		body.Set("resultType", wire.TextValue(resultTypeComplete))
	}
	return body
}

// inputRequiredBody builds the 2026-07-28 input_required result of a tool that asks the
// user, without the content member that Python omits.
func inputRequiredBody(result *mcp.CallToolResult) wire.Value {
	requests := wire.NewObject()
	for id, request := range result.InputRequests {
		elicit, ok := request.(*mcp.ElicitParams)
		if !ok {
			continue
		}
		schema, _ := json.Marshal(elicit.RequestedSchema)
		parsedSchema, _ := wire.Parse(schema)
		params := wire.NewObject()
		params.Set("message", wire.TextValue(elicit.Message))
		params.Set("mode", wire.TextValue(elicit.Mode))
		params.Set("requestedSchema", parsedSchema)
		entry := wire.NewObject()
		entry.Set("method", wire.TextValue("elicitation/create"))
		entry.Set("params", params)
		requests.Set(id, entry)
	}
	body := wire.NewObject()
	body.Set("inputRequests", requests)
	body.Set("requestState", wire.TextValue(result.RequestState))
	body.Set("resultType", wire.TextValue(resultTypeInputWait))
	return body
}

// resourceList lists every skill file as a Markdown resource.
func (shim *shimServer) resourceList() wire.Value {
	var resources []wire.Value
	for _, packaged := range shim.skills {
		for _, file := range packaged.files {
			resource := wire.NewObject()
			if file.uri == packaged.uri {
				resource.Set("description", wire.TextValue(packaged.description))
			}
			resource.Set("mimeType", wire.TextValue(markdownType))
			if file.uri == packaged.uri {
				resource.Set("name", wire.TextValue(packaged.name))
			} else {
				resource.Set("name", wire.TextValue(file.uri))
			}
			resource.Set("uri", wire.TextValue(file.uri))
			resources = append(resources, resource)
		}
	}
	return wire.ListValue(resources)
}

// readResource returns one skill file, or the Python shim's unknown-resource error.
func (shim *shimServer) readResource(request mcp.Request, modern bool) (mcp.Result, error) {
	read, ok := request.(*mcp.ReadResourceRequest)
	uri := ""
	if ok && read.Params != nil {
		uri = read.Params.URI
	}
	for _, packaged := range shim.skills {
		for _, file := range packaged.files {
			if file.uri != uri {
				continue
			}
			content := wire.NewObject()
			content.Set("mimeType", wire.TextValue(markdownType))
			content.Set("text", wire.TextValue(file.text))
			content.Set("uri", wire.TextValue(file.uri))
			return shim.newResult(cacheableList("contents", wire.ListValue([]wire.Value{content}), modern)), nil
		}
	}
	data := wire.NewObject()
	data.Set("uri", wire.TextValue(uri))
	return nil, &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: "Unknown resource: " + uri, Data: json.RawMessage(data.Compact())}
}
