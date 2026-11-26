package shim

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/witseko/agentcy/internal/wire"
	"github.com/witseko/agentcy/protocol"
)

const testWorkspace = "0000000000000000000000000000000000000000000000000000000000000000"

// fakeCore answers member requests from a table keyed by action and records them.
type fakeCore struct {
	mutex    sync.Mutex
	answers  map[string]string
	failures map[string]error
	requests []wire.Request
}

// Exchange returns the canned answer of the request's action.
func (core *fakeCore) Exchange(_ context.Context, request wire.Request) (wire.Value, error) {
	core.mutex.Lock()
	defer core.mutex.Unlock()
	core.requests = append(core.requests, request)
	if err := core.failures[request.Action()]; err != nil {
		return wire.Value{}, err
	}
	answer, found := core.answers[request.Action()]
	if !found {
		answer = "{}"
	}
	return wire.Parse([]byte(answer))
}

// actions lists the actions the core received.
func (core *fakeCore) actions() []string {
	core.mutex.Lock()
	defer core.mutex.Unlock()
	var actions []string
	for _, request := range core.requests {
		actions = append(actions, request.Action())
	}
	return actions
}

// rawClient speaks raw JSON-RPC to an in-memory shim and answers server requests with
// answerElicitation.
type rawClient struct {
	t                 *testing.T
	connection        mcp.Connection
	nextID            int64
	modern            bool
	capabilities      string
	answerElicitation string
	elicitations      []string
}

// connectRaw starts server on an in-memory transport and connects a raw client in the
// given era. capabilities is the client capability object.
func connectRaw(t *testing.T, server *mcp.Server, modern bool, capabilities string) *rawClient {
	t.Helper()
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	ctx := context.Background()
	session, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	connection, err := clientTransport.Connect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { connection.Close(); session.Close() })
	client := &rawClient{t: t, connection: connection, modern: modern, capabilities: capabilities}
	if modern {
		client.call("server/discover", `{}`)
	} else {
		client.call("initialize", `{"protocolVersion":"2025-11-25","capabilities":`+capabilities+`,"clientInfo":{"name":"test","version":"0"}}`)
		if err := connection.Write(ctx, &jsonrpc.Request{Method: "notifications/initialized", Params: json.RawMessage(`{}`)}); err != nil {
			t.Fatal(err)
		}
	}
	return client
}

// call sends one request and returns its raw result or its error.
func (client *rawClient) call(method, params string) (string, *jsonrpc.Error) {
	client.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if client.modern {
		params = client.withMeta(params)
	}
	client.nextID++
	id, _ := jsonrpc.MakeID(float64(client.nextID))
	if err := client.connection.Write(ctx, &jsonrpc.Request{ID: id, Method: method, Params: json.RawMessage(params)}); err != nil {
		client.t.Fatal(err)
	}
	for {
		message, err := client.connection.Read(ctx)
		if err != nil {
			client.t.Fatalf("read %s: %v", method, err)
		}
		switch typed := message.(type) {
		case *jsonrpc.Request:
			client.elicitations = append(client.elicitations, string(typed.Params))
			if err := client.connection.Write(ctx, &jsonrpc.Response{ID: typed.ID, Result: json.RawMessage(client.answerElicitation)}); err != nil {
				client.t.Fatal(err)
			}
		case *jsonrpc.Response:
			if typed.Error != nil {
				var wireError *jsonrpc.Error
				if errors.As(typed.Error, &wireError) {
					return "", wireError
				}
				client.t.Fatalf("%s: %v", method, typed.Error)
			}
			return string(typed.Result), nil
		}
	}
}

// withMeta adds the 2026-07-28 per-request metadata to params.
func (client *rawClient) withMeta(params string) string {
	object, err := wire.Parse([]byte(params))
	if err != nil {
		client.t.Fatal(err)
	}
	meta, _ := wire.Parse([]byte(`{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientInfo":{"name":"test","version":"0"},"io.modelcontextprotocol/clientCapabilities":` + client.capabilities + `}`))
	object.Set("_meta", meta)
	return object.Compact()
}

// newTestServer builds the main shim on a fake core.
func newTestServer(t *testing.T, core *fakeCore) *mcp.Server {
	t.Helper()
	server, err := NewServer(Config{Workspace: testWorkspace, Directory: "/tmp/checkout", Version: "9.9.9", Core: core})
	if err != nil {
		t.Fatal(err)
	}
	return server
}

// resultMember returns one member of a raw result.
func resultMember(t *testing.T, result, key string) wire.Value {
	t.Helper()
	value, _ := parse(t, result).Get(key)
	return value
}

// toolText returns the text of a tool result and whether it is an error.
func toolText(t *testing.T, result string) (string, bool) {
	t.Helper()
	content := resultMember(t, result, "content")
	if len(content.Items) != 1 {
		t.Fatalf("result %s has no single text block", result)
	}
	text, _ := content.Items[0].Get("text")
	return text.StringOf(), resultMember(t, result, "isError").Truthy()
}

func TestServedToolListEqualsTheGolden(t *testing.T) {
	for _, modern := range []bool{false, true} {
		client := connectRaw(t, newTestServer(t, &fakeCore{}), modern, `{}`)
		result, rpcError := client.call("tools/list", `{}`)
		if rpcError != nil {
			t.Fatal(rpcError)
		}
		golden := protocol.MCPToolsLegacy
		if modern {
			golden = protocol.MCPTools
		}
		want, _ := parse(t, string(golden)).Get("tools")
		if got := resultMember(t, result, "tools"); got.Compact() != want.Compact() {
			t.Errorf("modern=%v: served tools differ from the golden file", modern)
		}
		if modern && !strings.Contains(result, `"cacheScope":"private"`) {
			t.Errorf("modern tools/list lacks the private cache scope: %.200s", result)
		}
		if !modern && strings.Contains(result, "ttlMs") {
			t.Error("legacy tools/list carries cache hints")
		}
	}
}

func TestGoldenToolListsMatchTheTranscripts(t *testing.T) {
	for golden, transcript := range map[string]string{
		string(protocol.MCPTools):       "../../protocol/mcp-transcripts/tools-list.2026-07-28.json",
		string(protocol.MCPToolsLegacy): "../../protocol/mcp-transcripts/tools-list.2025-11-25.json",
	} {
		data, err := os.ReadFile(transcript)
		if err != nil {
			t.Fatal(err)
		}
		recorded, _ := parse(t, string(data)).Get("tools")
		embedded, _ := parse(t, golden).Get("tools")
		if recorded.Compact() != embedded.Compact() {
			t.Errorf("%s and the embedded golden differ", transcript)
		}
	}
}

func TestUnknownToolIsAnErrorResult(t *testing.T) {
	client := connectRaw(t, newTestServer(t, &fakeCore{}), false, `{}`)
	result, rpcError := client.call("tools/call", `{"name":"nope","arguments":{}}`)
	if rpcError != nil {
		t.Fatal(rpcError)
	}
	if text, isError := toolText(t, result); text != "Unknown tool: nope" || !isError {
		t.Errorf("result %s", result)
	}
}

func TestToolErrorsCarryThePythonPrefix(t *testing.T) {
	core := &fakeCore{failures: map[string]error{"post": &wire.CoreError{Message: "Conversation is closed"}}}
	client := connectRaw(t, newTestServer(t, core), false, `{}`)
	result, _ := client.call("tools/call", `{"name":"post_message","arguments":{"token":"t","submission_id":"s","body":"hi"}}`)
	if text, isError := toolText(t, result); text != "Error executing tool post_message: Conversation is closed" || !isError {
		t.Errorf("result %s", result)
	}
	result, _ = client.call("tools/call", `{"name":"read_messages","arguments":{}}`)
	text, isError := toolText(t, result)
	if !isError || !strings.HasPrefix(text, "Error executing tool read_messages: 1 validation error for read_messagesArguments\ntoken\n  Field required [type=missing]") {
		t.Errorf("validation text %q", text)
	}
}

func TestPostStripsTextAndSendsDefaults(t *testing.T) {
	core := &fakeCore{answers: map[string]string{"post": `{"message_id": 4}`}}
	client := connectRaw(t, newTestServer(t, core), false, `{}`)
	result, _ := client.call("tools/call", `{"name":"post_message","arguments":{"token":"t","submission_id":"s","body":"  hi\t ","to":"null","extra":1}}`)
	if text, isError := toolText(t, result); isError || text != "{\n  \"message_id\": 4\n}" {
		t.Errorf("result %s", result)
	}
	sent := core.requests[0].Encode().Compact()
	if !strings.Contains(sent, `"body":"hi","kind":"note","to":null`) {
		t.Errorf("sent %s", sent)
	}
}

func TestModernCloseAsksThenClosesWithTheSealedState(t *testing.T) {
	core := &fakeCore{answers: map[string]string{"close": `{"closed": true}`}}
	client := connectRaw(t, newTestServer(t, core), true, `{"elicitation":{"form":{}}}`)
	params := `{"name":"close_conversation","arguments":{"token":"t"}}`
	asked, _ := client.call("tools/call", params)
	if resultMember(t, asked, "resultType").StringOf() != "input_required" || strings.Contains(asked, `"content"`) {
		t.Fatalf("first answer %s", asked)
	}
	state := resultMember(t, asked, "requestState").StringOf()
	accept := `"inputResponses":{"` + confirmInputID + `":{"action":"accept","content":{"confirm":true}}}`
	unsealed, _ := client.call("tools/call", `{"name":"close_conversation","arguments":{"token":"t"},`+accept+`}`)
	if resultMember(t, unsealed, "resultType").StringOf() != "input_required" {
		t.Errorf("an answer without requestState was used: %s", unsealed)
	}
	_, rpcError := client.call("tools/call", `{"name":"close_conversation","arguments":{"token":"t"},`+accept+`,"requestState":"forged.state"}`)
	if rpcError == nil || rpcError.Code != jsonrpc.CodeInvalidParams || rpcError.Message != "Invalid or expired requestState" {
		t.Errorf("forged state: %v", rpcError)
	}
	_, rpcError = client.call("tools/call", `{"name":"close_conversation","arguments":{"token":"other"},`+accept+`,"requestState":"`+state+`"}`)
	if rpcError == nil {
		t.Error("a state sealed for other arguments was accepted")
	}
	if slices.Contains(core.actions(), "close") {
		t.Fatal("the core closed before a valid confirmation")
	}
	closed, _ := client.call("tools/call", `{"name":"close_conversation","arguments":{"token":"t"},`+accept+`,"requestState":"`+state+`"}`)
	if text, isError := toolText(t, closed); isError || text != "{\n  \"closed\": true\n}" {
		t.Errorf("closing answer %s", closed)
	}
}

func TestLegacyCloseElicitsInsideTheCall(t *testing.T) {
	for answer, want := range map[string]string{
		`{"action":"accept","content":{"confirm":true}}`:  "{\n  \"closed\": true\n}",
		`{"action":"accept","content":{"confirm":false}}`: "Error executing tool close_conversation: The user declined this action, so nothing changed. Do not retry it unless the user asks again.",
		`{"action":"cancel"}`:                             "Error executing tool close_conversation: The user cancelled this action, so nothing changed. Do not retry it unless the user asks again.",
	} {
		core := &fakeCore{answers: map[string]string{"close": `{"closed": true}`}}
		client := connectRaw(t, newTestServer(t, core), false, `{"elicitation":{}}`)
		client.answerElicitation = answer
		result, rpcError := client.call("tools/call", `{"name":"close_conversation","arguments":{"token":"t"}}`)
		if rpcError != nil {
			t.Fatal(rpcError)
		}
		if text, _ := toolText(t, result); text != want {
			t.Errorf("answer %s: text %q", answer, text)
		}
		if len(client.elicitations) != 1 || !strings.Contains(client.elicitations[0], `"title":"Confirmation"`) {
			t.Errorf("elicitations %v", client.elicitations)
		}
	}
}

func TestCloseWithoutElicitationClosesDirectly(t *testing.T) {
	core := &fakeCore{answers: map[string]string{"close": `{"closed": true}`}}
	client := connectRaw(t, newTestServer(t, core), true, `{}`)
	result, _ := client.call("tools/call", `{"name":"close_conversation","arguments":{"token":"t"}}`)
	if text, _ := toolText(t, result); text != "{\n  \"closed\": true\n}" {
		t.Errorf("result %s", result)
	}
}

func TestLaunchBecomesAnMcpTaskForATasksClient(t *testing.T) {
	launched := `{"participant": "Fresh", "mcp_task": {"task_id": "abc_DEF-1", "participant": "Fresh", "launch_state": "queued", "created": 0, "updated": 60}}`
	core := &fakeCore{answers: map[string]string{"launch": launched, "mcp_task_status": `{"participant": "Fresh", "launch_state": "failed", "created": 0, "updated": 60}`}}
	client := connectRaw(t, newTestServer(t, core), true, `{"extensions":{"io.modelcontextprotocol/tasks":{}}}`)
	result, _ := client.call("tools/call", `{"name":"launch_participant","arguments":{"token":"t","name":"Fresh","plugin":"fixture"}}`)
	want := `{"resultType":"task","taskId":"abc_DEF-1","createdAt":"1970-01-01T00:00:00Z","lastUpdatedAt":"1970-01-01T00:01:00Z","ttlMs":null,"pollIntervalMs":5000,"status":"working","statusMessage":"The fresh run for Fresh is queued.","_meta"`
	if !strings.HasPrefix(result, want) {
		t.Errorf("task result %s", result)
	}
	if launch := core.requests[0].(*wire.Launch); !launch.McpTask || launch.Directory != "/tmp/checkout" {
		t.Errorf("launch request %+v", launch)
	}
	task, _ := client.call("tasks/get", `{"taskId":"abc_DEF-1"}`)
	if !strings.Contains(task, `"status":"completed"`) || !strings.Contains(task, `"isError":true`) ||
		!strings.Contains(task, `"text":"{\"participant\": \"Fresh\", \"launch_state\": \"failed\", \"next\":`) {
		t.Errorf("tasks/get %s", task)
	}
}

func TestTaskMethodsFollowTheEraAndTheExtension(t *testing.T) {
	core := &fakeCore{failures: map[string]error{"mcp_task_status": &wire.CoreError{Message: unknownMcpTask}}}
	legacy := connectRaw(t, newTestServer(t, core), false, `{}`)
	if _, rpcError := legacy.call("tasks/get", `{"taskId":"x"}`); rpcError == nil || rpcError.Code != jsonrpc.CodeMethodNotFound {
		t.Errorf("legacy tasks/get: %v", rpcError)
	}
	undeclared := connectRaw(t, newTestServer(t, core), true, `{}`)
	if _, rpcError := undeclared.call("tasks/get", `{"taskId":"x"}`); rpcError == nil || rpcError.Code != missingCapability {
		t.Errorf("undeclared tasks/get: %v", rpcError)
	}
	declared := connectRaw(t, newTestServer(t, core), true, `{"extensions":{"io.modelcontextprotocol/tasks":{}}}`)
	for _, taskID := range []string{"x", "not a valid id!"} {
		_, rpcError := declared.call("tasks/get", `{"taskId":"`+taskID+`"}`)
		if rpcError == nil || rpcError.Code != jsonrpc.CodeInvalidParams || rpcError.Message != unknownMcpTask {
			t.Errorf("task %q: %v", taskID, rpcError)
		}
	}
}

func TestSkillsAndResourcesAreServed(t *testing.T) {
	modern := connectRaw(t, newTestServer(t, &fakeCore{}), true, `{}`)
	listed, _ := modern.call("skills/list", `{}`)
	if !strings.Contains(listed, `"ttlMs":300000,"cacheScope":"public","resultType":"complete"`) {
		t.Errorf("modern skills/list %.300s", listed)
	}
	legacy := connectRaw(t, newTestServer(t, &fakeCore{}), false, `{}`)
	legacyListed, _ := legacy.call("skills/list", `{}`)
	if strings.Contains(legacyListed, "ttlMs") || !strings.Contains(legacyListed, `"uri":"skill://lead-conversation/SKILL.md"`) {
		t.Errorf("legacy skills/list %.300s", legacyListed)
	}
	if _, rpcError := legacy.call("skills/get", `{"uri":"skill://nope"}`); rpcError == nil || rpcError.Message != "No skill is served at skill://nope" {
		t.Errorf("unknown skill: %v", rpcError)
	}
	read, _ := legacy.call("resources/read", `{"uri":"skill://join-conversation/SKILL.md"}`)
	if !strings.Contains(read, `"mimeType":"text/markdown","text":"---\nname: join-conversation`) {
		t.Errorf("resources/read %.200s", read)
	}
	if _, rpcError := legacy.call("resources/read", `{"uri":"skill://nope/SKILL.md"}`); rpcError == nil || rpcError.Message != "Unknown resource: skill://nope/SKILL.md" {
		t.Errorf("unknown resource: %v", rpcError)
	}
}

func TestFreshWorkerRequiresAJoinAndHidesTheToken(t *testing.T) {
	core := &fakeCore{answers: map[string]string{"join": `{"conversation": "c", "token": "secret-token", "brief": "B"}`, "post": `{"message_id": 1}`}}
	server, err := NewFreshWorkerServer(testWorkspace, "join-code", core)
	if err != nil {
		t.Fatal(err)
	}
	client := connectRaw(t, server, false, `{}`)
	result, _ := client.call("tools/call", `{"name":"read_messages","arguments":{}}`)
	if text, _ := toolText(t, result); text != "Error executing tool read_messages: Join your assigned conversation first" {
		t.Errorf("read before join: %q", text)
	}
	joined, _ := client.call("tools/call", `{"name":"join_conversation","arguments":{}}`)
	if strings.Contains(joined, "secret-token") || !strings.Contains(joined, `\"brief\": \"B\"`) {
		t.Errorf("join result %s", joined)
	}
	client.call("tools/call", `{"name":"join_conversation","arguments":{}}`)
	client.call("tools/call", `{"name":"post_message","arguments":{"submission_id":"s","body":"done"}}`)
	if got := strings.Join(core.actions(), ","); got != "join,post" {
		t.Errorf("core actions %s", got)
	}
	if post := core.requests[1].(*wire.Post); post.Token != "secret-token" {
		t.Errorf("post used token %q", post.Token)
	}
	tools, _ := client.call("tools/list", `{}`)
	want, _ := parse(t, string(protocol.FreshWorkerToolsLegacy)).Get("tools")
	if got := resultMember(t, tools, "tools"); got.Compact() != want.Compact() {
		t.Error("fresh worker tools differ from the golden file")
	}
}

// A waiting read whose page missed a close reads a fresh page: after one listen against
// a core with listen, and after the listen rejection, a read and a status against an
// older core.
func TestWaitingReadRereadsAPageThatMissedAClose(t *testing.T) {
	page := `{"messages": [], "more": false, "cursor": 0, "wake": false}`
	status := `{"open": false, "participant": "W", "members": {"W": {"role": "worker", "state": "replied"}}}`
	cases := []struct {
		name     string
		failures map[string]error
		want     string
	}{
		{"core with listen", nil, "listen,read"},
		{"core without listen", map[string]error{"listen": oldCoreRejection}, "listen,read,status,read"},
	}
	for _, test := range cases {
		core := &fakeCore{failures: test.failures, answers: map[string]string{
			"read": page, "status": status, "listen": `{"page": ` + page + `, "status": ` + status + `}`,
		}}
		client := connectRaw(t, newTestServer(t, core), false, `{}`)
		result, _ := client.call("tools/call", `{"name":"read_messages","arguments":{"token":"t","wait_seconds":1}}`)
		if got := strings.Join(core.actions(), ","); got != test.want {
			t.Errorf("%s: exchanges %s, want %s", test.name, got, test.want)
		}
		if text, _ := toolText(t, result); !strings.Contains(text, nextAfterClose) {
			t.Errorf("%s: text %q", test.name, text)
		}
	}
}
