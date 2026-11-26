package core

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/witseko/agentcy/internal/wire"
)

// Fixture values; none of them is a real credential.
const (
	testWorkspace = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	testToken     = "fake-member-token-0001"
	testRoomID    = "0123456789abcdef01234567"
)

// testRoom is the room of every fake participant.
var testRoom = Room{Workspace: testWorkspace, Conversation: testRoomID}

// fakeHandler answers with simple results unless a test sets one of its functions.
type fakeHandler struct {
	member         func(ctx context.Context, request wire.Request) (Outcome, error)
	operator       func(ctx context.Context, request wire.OperatorRequest) (Outcome, error)
	openWait       func(request *wire.Read) (WaitStart, error)
	openSession    func(request *wire.Session) (WaitStart, error)
	resolvePing    func(incoming wire.Incoming) (PingTarget, error)
	readPage       func(request *wire.Read) (wire.Value, error)
	recordDelivery func(page wire.Value) error
	finishListen   func(page wire.Value, deliver bool) (wire.Value, error)
	hasActiveWork  func() (bool, error)
	// filter is the wake filter of every page; nil wakes for every added message.
	filter WakeFilter
}

// resultNaming returns {"handled": action}.
func resultNaming(action string) Outcome {
	result := wire.NewObject()
	result.Set("handled", wire.TextValue(action))
	return Outcome{Result: result}
}

func (fake *fakeHandler) Member(ctx context.Context, request wire.Request, _ time.Time) (Outcome, error) {
	if fake.member != nil {
		return fake.member(ctx, request)
	}
	return resultNaming(request.Action()), nil
}

func (fake *fakeHandler) Operator(ctx context.Context, request wire.OperatorRequest, _ time.Time) (Outcome, error) {
	if fake.operator != nil {
		return fake.operator(ctx, request)
	}
	return resultNaming(request.Action()), nil
}

func (fake *fakeHandler) OpenWait(_ context.Context, request *wire.Read, _ time.Time) (WaitStart, error) {
	if fake.openWait != nil {
		return fake.openWait(request)
	}
	return WaitStart{Room: testRoom, MemberID: 1}, nil
}

func (fake *fakeHandler) OpenSession(_ context.Context, request *wire.Session, _ time.Time) (WaitStart, error) {
	if fake.openSession != nil {
		return fake.openSession(request)
	}
	return WaitStart{Room: testRoom, MemberID: 1}, nil
}

func (fake *fakeHandler) ResolveMemberPing(_ context.Context, request *wire.Ping, _ time.Time) (PingTarget, error) {
	if fake.resolvePing != nil {
		return fake.resolvePing(wire.Incoming{Member: request})
	}
	return PingTarget{Room: testRoom, MemberID: 1, Label: "Worker", State: "joined"}, nil
}

func (fake *fakeHandler) ResolveOperatorPing(_ context.Context, request *wire.OperatorPing, _ time.Time) (PingTarget, error) {
	if fake.resolvePing != nil {
		return fake.resolvePing(wire.Incoming{Operator: request})
	}
	return PingTarget{Room: testRoom, MemberID: 1, Label: "Worker", State: "joined"}, nil
}

func (fake *fakeHandler) ReadPage(_ context.Context, request *wire.Read, _ time.Time) (WaitPage, error) {
	if fake.readPage == nil {
		return waitPageFromValue(pageWith(0), fake.filter), nil
	}
	page, err := fake.readPage(request)
	return waitPageFromValue(page, fake.filter), err
}

// waitPageFromValue derives WaitPage's typed fields from a test fixture page value.
func waitPageFromValue(page wire.Value, filter WakeFilter) WaitPage {
	flag := func(name string) bool {
		value, _ := page.Get(name)
		return value.Truthy()
	}
	messages, _ := page.Get("messages")
	hasMessages := messages.Len() > 0
	closed, retired, more := flag("closed"), flag("retired"), flag("more")
	ready := closed || retired
	if !ready {
		if wake, found := page.Get("wake"); found {
			ready = wake.Truthy()
		} else {
			ready = hasMessages
		}
	}
	var roundDeadline *int64
	if value, found := page.Get("round_deadline"); found {
		if seconds, ok := value.IntOf(); ok {
			roundDeadline = &seconds
		}
	}
	return WaitPage{
		Page:          page,
		Filter:        filter,
		Ready:         ready,
		HasMessages:   hasMessages,
		EndsSettle:    closed || retired || more,
		RoundDeadline: roundDeadline,
	}
}

func (fake *fakeHandler) FinishListen(_ context.Context, _ *wire.Read, page wire.Value, deliver bool, _ time.Time) (wire.Value, error) {
	if fake.finishListen != nil {
		return fake.finishListen(page, deliver)
	}
	return resultNaming("status").Result, nil
}

func (fake *fakeHandler) RecordDelivery(_ context.Context, _ *wire.Read, page wire.Value, _ time.Time) error {
	if fake.recordDelivery != nil {
		return fake.recordDelivery(page)
	}
	return nil
}

func (fake *fakeHandler) HasActiveWork(context.Context, time.Time) (bool, error) {
	if fake.hasActiveWork != nil {
		return fake.hasActiveWork()
	}
	return true, nil
}

// pageWith returns a read page with count messages.
func pageWith(count int) wire.Value {
	messages := make([]wire.Value, count)
	for index := range messages {
		messages[index] = wire.IntValue(int64(index + 1))
	}
	page := wire.NewObject()
	page.Set("messages", wire.ListValue(messages))
	return page
}

// lockedBuffer is a log destination that several goroutines may write.
type lockedBuffer struct {
	mutex  sync.Mutex
	buffer bytes.Buffer
}

func (locked *lockedBuffer) Write(data []byte) (int, error) {
	locked.mutex.Lock()
	defer locked.mutex.Unlock()
	return locked.buffer.Write(data)
}

func (locked *lockedBuffer) String() string {
	locked.mutex.Lock()
	defer locked.mutex.Unlock()
	return locked.buffer.String()
}

// testDaemon is one daemon run in a goroutine of the test process.
type testDaemon struct {
	t       *testing.T
	dataDir string
	socket  string
	server  *server
	cancel  context.CancelFunc
	done    chan error
	logs    *lockedBuffer
}

// shortDataDir returns a new private directory under /tmp, because macOS limits socket
// paths to about 104 bytes and the default temporary directory is long.
func shortDataDir(t *testing.T) string {
	t.Helper()
	directory, err := os.MkdirTemp("/tmp", "agentcy-core-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(directory) })
	return directory
}

// startDaemon runs a daemon on a new data directory and waits until it accepts.
func startDaemon(t *testing.T, handler Handler, options Options) *testDaemon {
	t.Helper()
	return startDaemonIn(t, shortDataDir(t), handler, options)
}

// startDaemonIn runs a daemon on dataDir and waits until it accepts.
func startDaemonIn(t *testing.T, dataDir string, handler Handler, options Options) *testDaemon {
	t.Helper()
	daemon := launchDaemonIn(t, dataDir, handler, options)
	daemon.waitUntilServing()
	return daemon
}

// launchDaemonIn starts a daemon on dataDir without waiting for it.
func launchDaemonIn(t *testing.T, dataDir string, handler Handler, options Options) *testDaemon {
	t.Helper()
	logs := &lockedBuffer{}
	options.DataDir = dataDir
	options.Log = logs
	ctx, cancel := context.WithCancel(context.Background())
	daemon := &testDaemon{
		t:       t,
		dataDir: dataDir,
		socket:  filepath.Join(dataDir, socketFileName),
		server:  newServer(handler, options),
		cancel:  cancel,
		done:    make(chan error, 1),
		logs:    logs,
	}
	go func() { daemon.done <- daemon.server.run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-daemon.done:
		case <-time.After(10 * time.Second):
			t.Error("daemon did not stop")
		}
	})
	return daemon
}

// waitUntilServing waits up to 5 seconds for the socket to accept.
func (daemon *testDaemon) waitUntilServing() {
	daemon.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if connection, err := net.Dial("unix", daemon.socket); err == nil {
			connection.Close()
			return
		}
		select {
		case err := <-daemon.done:
			daemon.done <- err
			daemon.t.Fatalf("daemon stopped before serving: %v", err)
		case <-time.After(10 * time.Millisecond):
		}
	}
	daemon.t.Fatal("daemon did not start serving")
}

// stop ends the run and returns its error.
func (daemon *testDaemon) stop() error {
	daemon.t.Helper()
	daemon.cancel()
	return daemon.wait(10 * time.Second)
}

// wait returns the run's error, failing the test after timeout.
func (daemon *testDaemon) wait(timeout time.Duration) error {
	daemon.t.Helper()
	select {
	case err := <-daemon.done:
		daemon.done <- err
		return err
	case <-time.After(timeout):
		daemon.t.Fatal("daemon did not stop in time")
		return nil
	}
}

// operatorKeyText reads the key file.
func (daemon *testDaemon) operatorKeyText() string {
	daemon.t.Helper()
	data, err := os.ReadFile(filepath.Join(daemon.dataDir, operatorKeyFileName))
	if err != nil {
		daemon.t.Fatal(err)
	}
	return string(data)
}

// send writes payload and returns the whole reply, or "" when the daemon closes without
// one. With closeWrite the client half-closes after writing.
func (daemon *testDaemon) send(payload string, closeWrite bool) string {
	daemon.t.Helper()
	reply, err := exchange(daemon.socket, []byte(payload), closeWrite)
	if err != nil {
		daemon.t.Fatalf("exchange: %v", err)
	}
	return reply
}

// exchange performs one request on a new connection. The write runs beside the read, so
// a reply to an oversized line arrives even though the daemon stops reading.
func exchange(socket string, payload []byte, closeWrite bool) (string, error) {
	connection, err := net.Dial("unix", socket)
	if err != nil {
		return "", err
	}
	defer connection.Close()
	if err := connection.SetDeadline(time.Now().Add(60 * time.Second)); err != nil {
		return "", err
	}
	go func() {
		// The daemon may close before reading everything; the reply still matters.
		_, _ = connection.Write(payload)
		if closeWrite {
			_ = connection.(*net.UnixConn).CloseWrite()
		}
	}()
	reply, err := io.ReadAll(connection)
	if err != nil && !errors.Is(err, net.ErrClosed) && !isReset(err) {
		return string(reply), err
	}
	return string(reply), nil
}

// isReset reports a connection reset, which a client sees when the daemon closes with
// unread input after replying.
func isReset(err error) bool {
	return strings.Contains(err.Error(), "connection reset")
}

// line encodes fields as one compact request line.
func line(fields ...any) string {
	object := wire.NewObject()
	for index := 0; index < len(fields); index += 2 {
		object.Set(fields[index].(string), toValue(fields[index+1]))
	}
	return object.Compact() + "\n"
}

// toValue converts a few Go values to wire values for request lines.
func toValue(value any) wire.Value {
	switch typed := value.(type) {
	case string:
		return wire.TextValue(typed)
	case int:
		return wire.IntValue(int64(typed))
	case bool:
		return wire.BoolValue(typed)
	case nil:
		return wire.NullValue()
	default:
		panic("unsupported test value")
	}
}

// statusLine is a valid member status request.
func statusLine() string {
	return line("action", "status", "workspace", testWorkspace, "token", testToken)
}

// readLine is a valid read request that waits up to waitSeconds.
func readLine(waitSeconds int) string {
	return line("action", "read", "workspace", testWorkspace, "token", testToken, "wait_seconds", waitSeconds)
}

// errorOf returns the "error" text of a reply line, or "" when it has none.
func errorOf(t *testing.T, reply string) string {
	t.Helper()
	value := parseReply(t, reply)
	message, _ := value.Get("error")
	return message.StringOf()
}

// resultOf returns the "result" value of a reply line and fails without one.
func resultOf(t *testing.T, reply string) wire.Value {
	t.Helper()
	value := parseReply(t, reply)
	result, found := value.Get("result")
	if !found {
		t.Fatalf("reply %q has no result", reply)
	}
	return result
}

// parseReply checks that reply is exactly one JSON object line.
func parseReply(t *testing.T, reply string) wire.Value {
	t.Helper()
	if !strings.HasSuffix(reply, "\n") || strings.Count(reply, "\n") != 1 {
		t.Fatalf("reply %q is not one line", reply)
	}
	value, err := wire.Parse([]byte(reply))
	if err != nil || value.Kind != wire.Object {
		t.Fatalf("reply %q is not a JSON object", reply)
	}
	return value
}

// eventually polls condition for up to timeout.
func eventually(t *testing.T, timeout time.Duration, condition func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return condition()
}
