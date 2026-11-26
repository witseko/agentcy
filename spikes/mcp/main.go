// Command spike-mcp is a throwaway MCP server that checks whether the official Go MCP
// SDK can carry the agentcy shim's MCP surface: the shim's tool list verbatim, a form
// elicitation to confirm a close, a task-shaped tools/call result (MCP Tasks extension
// SEP-2663), tasks/get and tasks/cancel, and skills/list and skills/get (MCP Skills
// extension SEP-2640), in protocol eras 2025-11-25 and 2026-07-28, over stdio and
// streamable HTTP behind a bearer gate.
//
// Usage:
//
//	spike-mcp -tools PATH                 serve on stdio
//	spike-mcp -tools PATH -http PORT      serve http://127.0.0.1:PORT/mcp; the bearer
//	                                      token comes from SPIKE_HTTP_TOKEN
package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	tasksExtension     = "io.modelcontextprotocol/tasks"
	skillsExtension    = "io.modelcontextprotocol/skills"
	modernEra          = "2026-07-28"
	confirmInputID     = "agentcy.mcp.confirmation:confirm_close"
	launchTool         = "launch_participant"
	closeTool          = "close_conversation"
	accessTokenEnv     = "SPIKE_HTTP_TOKEN"
	minAccessTokenSize = 32
	unknownTaskMessage = "Unknown MCP task"
	spikeTaskID        = "spike-task-0001"
)

// goldenTool is one entry of a recorded tools/list result. The schemas stay raw JSON so
// they reach the wire byte for byte as recorded.
type goldenTool struct {
	Name         string               `json:"name"`
	Title        string               `json:"title,omitempty"`
	Description  string               `json:"description,omitempty"`
	InputSchema  json.RawMessage      `json:"inputSchema"`
	OutputSchema json.RawMessage      `json:"outputSchema,omitempty"`
	Annotations  *mcp.ToolAnnotations `json:"annotations,omitempty"`
}

// skillEntry is the wire form of one skill in skills/list and skills/get.
type skillEntry struct {
	URI         string            `json:"uri"`
	Frontmatter map[string]string `json:"frontmatter"`
	Resources   []skillResource   `json:"resources"`
}

type skillResource struct {
	URI    string `json:"uri"`
	Digest string `json:"digest"`
	Size   int    `json:"size"`
}

// ListSkillsParams are the parameters of skills/list.
type ListSkillsParams struct {
	mcp.ParamsBase
	Cursor string `json:"cursor,omitempty"`
}

// ListSkillsResult is the result of skills/list.
type ListSkillsResult struct {
	mcp.ResultBase
	Skills     []skillEntry `json:"skills"`
	TTLMs      *int         `json:"ttlMs,omitempty"`
	CacheScope string       `json:"cacheScope,omitempty"`
	ResultType string       `json:"resultType,omitempty"`
}

// GetSkillParams are the parameters of skills/get.
type GetSkillParams struct {
	mcp.ParamsBase
	URI string `json:"uri"`
}

// GetSkillResult is the result of skills/get.
type GetSkillResult struct {
	mcp.ResultBase
	Skill      skillEntry `json:"skill"`
	TTLMs      *int       `json:"ttlMs,omitempty"`
	CacheScope string     `json:"cacheScope,omitempty"`
	ResultType string     `json:"resultType,omitempty"`
}

// TaskParams are the parameters of tasks/get and tasks/cancel.
type TaskParams struct {
	mcp.ParamsBase
	TaskID string `json:"taskId"`
}

// Task is the wire form of an MCP task. As a tools/call result it carries
// resultType "task"; as a tasks/get result it carries "complete".
type Task struct {
	mcp.ResultBase
	ResultType     string `json:"resultType"`
	TaskID         string `json:"taskId"`
	CreatedAt      string `json:"createdAt"`
	LastUpdatedAt  string `json:"lastUpdatedAt"`
	TTLMs          *int   `json:"ttlMs"`
	PollIntervalMs int    `json:"pollIntervalMs"`
	Status         string `json:"status"`
	StatusMessage  string `json:"statusMessage,omitempty"`
}

// EmptyResult is the result of tasks/cancel.
type EmptyResult struct {
	mcp.ResultBase
	ResultType string `json:"resultType,omitempty"`
}

// metaString reads one string from a request's _meta.
func metaString(meta map[string]any, key string) string {
	value, _ := meta[key].(string)
	return value
}

// requestEra returns the protocol version of one request: the per-request _meta value in
// the 2026-07-28 era, else the version negotiated by initialize.
func requestEra(session *mcp.ServerSession, meta map[string]any) string {
	if era := metaString(meta, mcp.MetaKeyProtocolVersion); era != "" {
		return era
	}
	if params := session.InitializeParams(); params != nil {
		return params.ProtocolVersion
	}
	return ""
}

// requestDeclaresExtension tells whether the per-request client capabilities of a
// 2026-07-28 request name the extension.
func requestDeclaresExtension(meta map[string]any, extension string) bool {
	capabilities, _ := meta[mcp.MetaKeyClientCapabilities].(map[string]any)
	extensions, _ := capabilities["extensions"].(map[string]any)
	_, ok := extensions[extension]
	return ok
}

// completeIfModern returns the resultType that 2026-07-28 requires and older eras omit.
func completeIfModern(era string) string {
	if era == modernEra {
		return "complete"
	}
	return ""
}

func methodNotFound(method string) error {
	return &jsonrpc.Error{Code: jsonrpc.CodeMethodNotFound, Message: "Method not found", Data: json.RawMessage(strconv.Quote(method))}
}

// spikeTask returns the one fake MCP task this spike knows.
func spikeTask(resultType string) *Task {
	now := time.Now().UTC().Format("2006-01-02T15:04:05Z")
	return &Task{
		ResultType:     resultType,
		TaskID:         spikeTaskID,
		CreatedAt:      now,
		LastUpdatedAt:  now,
		PollIntervalMs: 5000,
		Status:         "working",
		StatusMessage:  "The fresh run for Fresh is queued.",
	}
}

// declaresFormElicitation tells whether a request may get a form elicitation: a bare
// `elicitation: {}` or one with `form` qualifies, URL-only does not.
func declaresFormElicitation(capabilities *mcp.ClientCapabilities) bool {
	if capabilities == nil || capabilities.Elicitation == nil {
		return false
	}
	return capabilities.Elicitation.Form != nil || capabilities.Elicitation.URL == nil
}

func textResult(text string, isError bool) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}, IsError: isError}
}

// closeHandler asks for a form confirmation through a multi round-trip input request
// (SEP-2322). The SDK sends it as an input_required result in 2026-07-28 and turns it
// into an elicitation/create request for older clients.
func closeHandler(ctx context.Context, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	if response, ok := request.Params.InputResponses[confirmInputID]; ok {
		answer, _ := response.(*mcp.ElicitResult)
		if answer != nil && answer.Action == "accept" && answer.Content["confirm"] == true {
			return textResult("{\n  \"closed\": true\n}", false), nil
		}
		verb := "declined"
		if answer != nil && answer.Action == "cancel" {
			verb = "cancelled"
		}
		return textResult("The user "+verb+" this action, so nothing changed. Do not retry it unless the user asks again.", true), nil
	}
	if !declaresFormElicitation(request.ClientCapabilities()) {
		return textResult("{\n  \"closed\": true\n}", false), nil
	}
	schema := json.RawMessage(`{"type":"object","properties":{"confirm":{"type":"boolean","title":"Confirm","description":"Select to confirm this action."}},"required":["confirm"]}`)
	return &mcp.CallToolResult{
		InputRequests: mcp.InputRequestMap{
			confirmInputID: &mcp.ElicitParams{Mode: "form", Message: "Close this conversation?", RequestedSchema: schema},
		},
	}, nil
}

// stubHandler answers every other recorded tool with a fixed JSON text result.
func stubHandler(ctx context.Context, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return textResult(fmt.Sprintf("{\"tool\": %q}", request.Params.Name), false), nil
}

// taskResultMiddleware replaces the launch_participant result with an MCP task when the
// request is a 2026-07-28 request that declares the Tasks extension. This mirrors
// McpTasksExtension.intercept_tool_call in src/agentcy/mcp/tasks.py.
func taskResultMiddleware(next mcp.MethodHandler) mcp.MethodHandler {
	return func(ctx context.Context, method string, request mcp.Request) (mcp.Result, error) {
		result, err := next(ctx, method, request)
		if err != nil || method != "tools/call" {
			return result, err
		}
		call, ok := request.(*mcp.CallToolRequest)
		if !ok || call.Params.Name != launchTool {
			return result, err
		}
		meta := call.Params.GetMeta()
		if call.ProtocolVersion() == modernEra && requestDeclaresExtension(meta, tasksExtension) {
			return spikeTask("task"), nil
		}
		return result, err
	}
}

func loadTools(path string) ([]goldenTool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var listing struct {
		Tools []goldenTool `json:"tools"`
	}
	if err := json.Unmarshal(data, &listing); err != nil {
		return nil, err
	}
	return listing.Tools, nil
}

func newServer(tools []goldenTool) (*mcp.Server, error) {
	capabilities := &mcp.ServerCapabilities{
		Tools:     &mcp.ToolCapabilities{},
		Resources: &mcp.ResourceCapabilities{},
		Prompts:   &mcp.PromptCapabilities{},
	}
	capabilities.AddExtension(skillsExtension, nil)
	capabilities.AddExtension(tasksExtension, nil)
	server := mcp.NewServer(
		&mcp.Implementation{Name: "agentcy", Version: "0.0.0-spike"},
		&mcp.ServerOptions{Instructions: "Spike server instructions.", Capabilities: capabilities},
	)
	for _, tool := range tools {
		handler := stubHandler
		if tool.Name == closeTool {
			handler = closeHandler
		}
		entry := &mcp.Tool{Name: tool.Name, Title: tool.Title, Description: tool.Description, InputSchema: tool.InputSchema, Annotations: tool.Annotations}
		if len(tool.OutputSchema) > 0 {
			entry.OutputSchema = tool.OutputSchema
		}
		server.AddTool(entry, handler)
	}
	server.AddReceivingMiddleware(taskResultMiddleware)

	skill := skillEntry{
		URI:         "skill://lead-conversation/SKILL.md",
		Frontmatter: map[string]string{"name": "lead-conversation", "description": "Spike skill."},
		Resources:   []skillResource{{URI: "skill://lead-conversation/SKILL.md", Digest: "sha256:00", Size: 1}},
	}
	cacheTTL := 300000
	err := errors.Join(
		mcp.AddReceivingCustomMethod(server, "skills/list",
			func(ctx context.Context, session *mcp.ServerSession, params *ListSkillsParams) (*ListSkillsResult, error) {
				if params == nil { // a legacy request may omit params
					params = &ListSkillsParams{}
				}
				result := &ListSkillsResult{Skills: []skillEntry{skill}}
				if era := requestEra(session, params.GetMeta()); era == modernEra {
					result.TTLMs, result.CacheScope, result.ResultType = &cacheTTL, "public", "complete"
				}
				return result, nil
			}),
		mcp.AddReceivingCustomMethod(server, "skills/get",
			func(ctx context.Context, session *mcp.ServerSession, params *GetSkillParams) (*GetSkillResult, error) {
				if params == nil { // a legacy request may omit params
					params = &GetSkillParams{}
				}
				if params.URI != skill.URI {
					return nil, &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: "No skill is served at " + params.URI}
				}
				result := &GetSkillResult{Skill: skill}
				if era := requestEra(session, params.GetMeta()); era == modernEra {
					result.TTLMs, result.CacheScope, result.ResultType = &cacheTTL, "public", "complete"
				}
				return result, nil
			}),
		mcp.AddReceivingCustomMethod(server, "tasks/get",
			func(ctx context.Context, session *mcp.ServerSession, params *TaskParams) (*Task, error) {
				if params == nil { // a legacy request may omit params
					params = &TaskParams{}
				}
				meta := params.GetMeta()
				if requestEra(session, meta) != modernEra {
					return nil, methodNotFound("tasks/get")
				}
				if !requestDeclaresExtension(meta, tasksExtension) {
					return nil, &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: "Missing required client extension " + tasksExtension}
				}
				if params.TaskID != spikeTaskID {
					return nil, &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: unknownTaskMessage}
				}
				return spikeTask("complete"), nil
			}),
		mcp.AddReceivingCustomMethod(server, "tasks/cancel",
			func(ctx context.Context, session *mcp.ServerSession, params *TaskParams) (*EmptyResult, error) {
				if params == nil { // a legacy request may omit params
					params = &TaskParams{}
				}
				meta := params.GetMeta()
				if requestEra(session, meta) != modernEra {
					return nil, methodNotFound("tasks/cancel")
				}
				return &EmptyResult{ResultType: completeIfModern(modernEra)}, nil
			}),
	)
	return server, err
}

// bearerGate passes a request on only when it carries the access token.
func bearerGate(next http.Handler, token string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		scheme, credential, _ := strings.Cut(r.Header.Get("Authorization"), " ")
		if !strings.EqualFold(scheme, "bearer") || subtle.ConstantTimeCompare([]byte(strings.TrimSpace(credential)), []byte(token)) != 1 {
			w.Header().Set("WWW-Authenticate", "Bearer")
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte("Unauthorized"))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// eraRouter sends 2026-07-28 requests to a stateless handler and older ones to a stateful
// handler. The SDK serves 2026-07-28 over HTTP only in stateless mode, while a
// 2025-11-25 elicitation needs the session of a stateful handler. Every 2026-07-28
// request, server/discover included, carries the MCP-Protocol-Version header.
func eraRouter(server *mcp.Server) http.Handler {
	getServer := func(*http.Request) *mcp.Server { return server }
	stateful := mcp.NewStreamableHTTPHandler(getServer, nil)
	stateless := mcp.NewStreamableHTTPHandler(getServer, &mcp.StreamableHTTPOptions{Stateless: true})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("MCP-Protocol-Version") >= modernEra {
			stateless.ServeHTTP(w, r)
			return
		}
		stateful.ServeHTTP(w, r)
	})
}

func main() {
	toolsPath := flag.String("tools", "", "path to a recorded tools/list result")
	port := flag.Int("http", 0, "serve streamable HTTP on 127.0.0.1:PORT instead of stdio")
	flag.Parse()
	tools, err := loadTools(*toolsPath)
	if err != nil {
		log.Fatalf("load tools: %v", err)
	}
	server, err := newServer(tools)
	if err != nil {
		log.Fatalf("build server: %v", err)
	}
	if *port == 0 {
		if err := server.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
			log.Fatalf("stdio: %v", err)
		}
		return
	}
	token := os.Getenv(accessTokenEnv)
	os.Unsetenv(accessTokenEnv)
	if len(token) < minAccessTokenSize {
		log.Fatalf("set %s to at least %d characters", accessTokenEnv, minAccessTokenSize)
	}
	mux := http.NewServeMux()
	mux.Handle("/mcp", bearerGate(eraRouter(server), token))
	address := fmt.Sprintf("127.0.0.1:%d", *port)
	log.Fatal((&http.Server{Addr: address, Handler: mux, ReadHeaderTimeout: 10 * time.Second}).ListenAndServe())
}
