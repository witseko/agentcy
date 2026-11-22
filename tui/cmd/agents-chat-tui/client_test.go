package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"
)

// fakeCore serves scripted replies on a Unix socket and records each request line.
type fakeCore struct {
	t        *testing.T
	home     string
	listener net.Listener
	reply    func(request map[string]any) string
	mutex    sync.Mutex
	requests []map[string]any
}

// shortHome returns a private directory under /tmp, because macOS limits Unix socket paths
// to about 104 bytes and the default test directory is longer.
func shortHome(t *testing.T) string {
	t.Helper()
	home, err := os.MkdirTemp("/tmp", "act-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(home) })
	return home
}

// writeKey writes the operator key file the way the core does.
func writeKey(t *testing.T, home string, key string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(home, operatorKeyFile), []byte(key), 0o600); err != nil {
		t.Fatal(err)
	}
}

// startFakeCore listens on core.sock in a short home with the key "first-key".
func startFakeCore(t *testing.T, reply func(request map[string]any) string) *fakeCore {
	t.Helper()
	home := shortHome(t)
	writeKey(t, home, "first-key\n")
	listener, err := net.Listen("unix", filepath.Join(home, socketFile))
	if err != nil {
		t.Fatal(err)
	}
	core := &fakeCore{t: t, home: home, listener: listener, reply: reply}
	t.Cleanup(func() { listener.Close() })
	go core.serve()
	return core
}

// serve answers one request per connection until the listener closes.
func (f *fakeCore) serve() {
	for {
		connection, err := f.listener.Accept()
		if err != nil {
			return
		}
		go f.answer(connection)
	}
}

// answer records one request line and writes the scripted reply.
func (f *fakeCore) answer(connection net.Conn) {
	defer connection.Close()
	line, err := bufio.NewReader(connection).ReadBytes('\n')
	if err != nil {
		return
	}
	var request map[string]any
	if err := json.Unmarshal(line, &request); err != nil {
		f.t.Errorf("request is not JSON: %v", err)
		return
	}
	f.mutex.Lock()
	f.requests = append(f.requests, request)
	f.mutex.Unlock()
	reply := f.reply(request)
	if reply == "" {
		time.Sleep(time.Second)
		return
	}
	connection.Write([]byte(reply + "\n"))
}

// recorded returns a copy of the recorded requests.
func (f *fakeCore) recorded() []map[string]any {
	f.mutex.Lock()
	defer f.mutex.Unlock()
	return append([]map[string]any(nil), f.requests...)
}

func TestPostSendsOneVersionedLineWithTheKey(t *testing.T) {
	core := startFakeCore(t, func(map[string]any) string { return `{"result": {"message_id": 7}}` })
	client := NewClient(core.home)

	posted, err := client.Post(PostRequest{
		Directory: "/work", Conversation: "0123456789abcdef01234567",
		SubmissionID: "ui-1", Body: "Check the limit.", Kind: "question",
	})

	if err != nil || posted.MessageID != 7 {
		t.Fatalf("Post() = %+v, %v", posted, err)
	}
	want := map[string]any{
		"version": 1.0, "key": "first-key", "action": "operator_post", "directory": "/work",
		"conversation": "0123456789abcdef01234567", "submission_id": "ui-1",
		"body": "Check the limit.", "kind": "question",
	}
	if got := core.recorded(); len(got) != 1 || !reflect.DeepEqual(got[0], want) {
		t.Fatalf("requests = %v, want [%v]", got, want)
	}
}

func TestCoreErrorBecomesCoreError(t *testing.T) {
	core := startFakeCore(t, func(map[string]any) string {
		return `{"error": "Unknown conversation in this workspace"}`
	})

	_, err := NewClient(core.home).Show(ShowRequest{Directory: "/work", Conversation: "x"})

	var coreError *CoreError
	if !errors.As(err, &coreError) || coreError.Message != "Unknown conversation in this workspace" {
		t.Fatalf("Show() error = %v, want the core's message", err)
	}
}

func TestRejectedKeyIsReadAgainOnce(t *testing.T) {
	var core *fakeCore
	core = startFakeCore(t, func(request map[string]any) string {
		if request["key"] != "second-key" {
			// The core restarted and wrote a new key after the client read the old one.
			writeKey(t, core.home, "second-key")
			return `{"error": "Operator key rejected"}`
		}
		return `{"result": {"plugins": []}}`
	})

	_, err := NewClient(core.home).Plugins()

	if err != nil {
		t.Fatalf("Plugins() error = %v", err)
	}
	keys := []any{}
	for _, request := range core.recorded() {
		keys = append(keys, request["key"])
	}
	if !reflect.DeepEqual(keys, []any{"first-key", "second-key"}) {
		t.Fatalf("keys sent = %v", keys)
	}
}

func TestRejectedKeyIsRetriedOnlyOnce(t *testing.T) {
	core := startFakeCore(t, func(map[string]any) string { return `{"error": "Operator key rejected"}` })

	_, err := NewClient(core.home).Plugins()

	var coreError *CoreError
	if !errors.As(err, &coreError) || len(core.recorded()) != 2 {
		t.Fatalf("Plugins() error = %v after %d requests", err, len(core.recorded()))
	}
}

func TestSilentCoreTimesOut(t *testing.T) {
	core := startFakeCore(t, func(map[string]any) string { return "" })
	client := NewClient(core.home)
	client.timeout = 100 * time.Millisecond

	started := time.Now()
	_, err := client.List(ListRequest{Directory: "/work"})

	if err == nil || time.Since(started) > 900*time.Millisecond {
		t.Fatalf("List() error = %v after %v", err, time.Since(started))
	}
}

func TestMissingSocketIsUnreachable(t *testing.T) {
	home := shortHome(t)
	writeKey(t, home, "key")

	_, err := NewClient(home).List(ListRequest{Directory: "/work"})

	if !errors.Is(err, errCoreUnreachable) {
		t.Fatalf("List() error = %v, want errCoreUnreachable", err)
	}
}

func TestMalformedResponseIsAnError(t *testing.T) {
	core := startFakeCore(t, func(map[string]any) string { return `not json` })

	_, err := NewClient(core.home).List(ListRequest{Directory: "/work"})

	if err == nil {
		t.Fatal("List() accepted a malformed response")
	}
}

// exampleTypes maps each operator action to its Go request and result types.
var exampleTypes = map[string][2]reflect.Type{
	"operator_plugins": {reflect.TypeFor[PluginsRequest](), reflect.TypeFor[PluginList]()},
	"operator_list":    {reflect.TypeFor[ListRequest](), reflect.TypeFor[ConversationList]()},
	"operator_show":    {reflect.TypeFor[ShowRequest](), reflect.TypeFor[ConversationView]()},
	"operator_create":  {reflect.TypeFor[CreateRequest](), reflect.TypeFor[CreatedConversation]()},
	"operator_invite":  {reflect.TypeFor[InviteRequest](), reflect.TypeFor[JoinInvitation]()},
	"operator_launch":  {reflect.TypeFor[LaunchRequest](), reflect.TypeFor[LaunchQueued]()},
	"operator_retire":   {reflect.TypeFor[RetireRequest](), reflect.TypeFor[RetiredParticipant]()},
	"operator_set_role": {reflect.TypeFor[SetRoleRequest](), reflect.TypeFor[ParticipantRoleChanged]()},
	"operator_post":    {reflect.TypeFor[PostRequest](), reflect.TypeFor[PostedMessage]()},
	"operator_close":   {reflect.TypeFor[CloseRequest](), reflect.TypeFor[ClosedConversation]()},
	"operator_archive": {reflect.TypeFor[ArchiveRequest](), reflect.TypeFor[ArchivedConversation]()},
	"operator_search":  {reflect.TypeFor[SearchRequest](), reflect.TypeFor[SearchResults]()},
}

// example is one exchange from protocol/operator.v1.examples.json.
type example struct {
	Action  string          `json:"action"`
	Request json.RawMessage `json:"request"`
	Result  json.RawMessage `json:"result"`
}

// strictRoundTrip decodes raw into a new value of kind, rejecting unknown fields, and
// returns the value with its re-encoded JSON as a generic value.
func strictRoundTrip(t *testing.T, raw []byte, kind reflect.Type) (any, any) {
	t.Helper()
	value := reflect.New(kind)
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value.Interface()); err != nil {
		t.Fatalf("decoding into %v: %v", kind, err)
	}
	encoded, err := json.Marshal(value.Interface())
	if err != nil {
		t.Fatal(err)
	}
	var generic any
	if err := json.Unmarshal(encoded, &generic); err != nil {
		t.Fatal(err)
	}
	return value.Elem().Interface(), generic
}

// generic decodes JSON into maps, slices and float64 values for comparison.
func generic(t *testing.T, raw []byte) any {
	t.Helper()
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatal(err)
	}
	return value
}

func TestGoTypesMatchTheProtocolExamples(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "protocol", "operator.v1.examples.json"))
	if err != nil {
		t.Fatal(err)
	}
	var examples []example
	if err := json.Unmarshal(raw, &examples); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, exchange := range examples {
		types, known := exampleTypes[exchange.Action]
		if !known {
			t.Fatalf("no Go types for action %s", exchange.Action)
		}
		seen[exchange.Action] = true
		requestFields := generic(t, exchange.Request).(map[string]any)
		key := requestFields["key"].(string)
		for _, field := range []string{"version", "key", "action"} {
			delete(requestFields, field)
		}
		body, _ := json.Marshal(requestFields)
		request, _ := strictRoundTrip(t, body, types[0])
		line, err := encodeRequest(exchange.Action, key, request)
		if err != nil {
			t.Fatal(err)
		}
		if got, want := generic(t, line), generic(t, exchange.Request); !reflect.DeepEqual(got, want) {
			t.Errorf("%s request encodes as %v, want %v", exchange.Action, got, want)
		}
		_, result := strictRoundTrip(t, exchange.Result, types[1])
		if want := generic(t, exchange.Result); !reflect.DeepEqual(result, want) {
			t.Errorf("%s result re-encodes as %v, want %v", exchange.Action, result, want)
		}
	}
	if len(seen) != len(exampleTypes) {
		t.Errorf("examples cover %d of %d actions", len(seen), len(exampleTypes))
	}
}
