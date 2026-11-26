package core

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/witseko/agentcy/internal/wire"
)

// paddedStatusLine builds a status request padded with JSON whitespace to exactly size
// bytes including the newline, like the conformance suite.
func paddedStatusLine(size int) string {
	prefix := fmt.Sprintf(`{"action":"status","workspace":"%s","token":"unknown"`, testWorkspace)
	return prefix + strings.Repeat(" ", size-len(prefix)-len("}\n")) + "}\n"
}

// rejectingStatus answers every member request as an unknown token, like the real core.
func rejectingStatus() *fakeHandler {
	return &fakeHandler{member: func(context.Context, wire.Request) (Outcome, error) {
		return Outcome{}, Reject("Invalid participant token")
	}}
}

func TestRequestLineSizeLimit(t *testing.T) {
	daemon := startDaemon(t, rejectingStatus(), Options{})
	cases := []struct {
		name  string
		size  int
		error string
	}{
		{"exactly 131072 bytes with the newline", wire.MaxRequestBytes, "Invalid participant token"},
		{"131073 bytes with the newline", wire.MaxRequestBytes + 1, requestTooLarge},
		{"300000 bytes", 300000, requestTooLarge},
	}
	for _, entry := range cases {
		if got := errorOf(t, daemon.send(paddedStatusLine(entry.size), false)); got != entry.error {
			t.Errorf("%s: error %q, want %q", entry.name, got, entry.error)
		}
	}
}

func TestUnterminatedLineThenHalfCloseExceedsSizeLimit(t *testing.T) {
	daemon := startDaemon(t, &fakeHandler{}, Options{})
	reply := daemon.send(strings.TrimSuffix(statusLine(), "\n"), true)
	if got := errorOf(t, reply); got != requestTooLarge {
		t.Errorf("error %q, want %q", got, requestTooLarge)
	}
}

func TestEmptyHalfClosedConnectionGetsNoReply(t *testing.T) {
	daemon := startDaemon(t, &fakeHandler{}, Options{})
	if reply := daemon.send("", true); reply != "" {
		t.Errorf("reply %q, want none", reply)
	}
}

func TestLineEndingsAndExtraLines(t *testing.T) {
	var calls atomic.Int32
	handler := &fakeHandler{member: func(_ context.Context, request wire.Request) (Outcome, error) {
		calls.Add(1)
		return resultNaming(request.Action()), nil
	}}
	daemon := startDaemon(t, handler, Options{})
	crlf := strings.TrimSuffix(statusLine(), "\n") + "\r\n"
	if handled, _ := resultOf(t, daemon.send(crlf, false)).Get("handled"); handled.StringOf() != "status" {
		t.Errorf("CRLF line handled as %v", handled)
	}
	twoLines := statusLine() + line("action", "explode") + strings.Repeat(" ", 10)
	if handled, _ := resultOf(t, daemon.send(twoLines, false)).Get("handled"); handled.StringOf() != "status" {
		t.Errorf("first of two lines handled as %v", handled)
	}
	if calls.Load() != 2 {
		t.Errorf("handler ran %d times, want 2: the second line must be ignored", calls.Load())
	}
}

func TestMalformedLinesAreInvalidBridgeArguments(t *testing.T) {
	daemon := startDaemon(t, &fakeHandler{}, Options{})
	status := strings.TrimSuffix(statusLine(), "\n")
	cases := map[string]string{
		"an empty line":              "\n",
		"a line of spaces":           "   \n",
		"invalid JSON":               "{not json\n",
		"a JSON array":               "[1, 2]\n",
		"null":                       "null\n",
		"two objects on one line":    status + " {}\n",
		"invalid UTF-8 in a string":  strings.Replace(status, testToken, testToken+"\xff", 1) + "\n",
		"an unknown action":          line("action", "explode", "workspace", testWorkspace),
		"a member request bad field": line("action", "status", "workspace", "abc", "token", testToken),
	}
	for name, payload := range cases {
		if got := errorOf(t, daemon.send(payload, false)); got != wire.InvalidArguments {
			t.Errorf("%s: error %q, want %q", name, got, wire.InvalidArguments)
		}
	}
}

func TestStalledRequestTimesOutWithoutReplyAndDaemonKeepsServing(t *testing.T) {
	t.Parallel()
	daemon := startDaemon(t, &fakeHandler{}, Options{})
	stalled := make(chan string, 1)
	started := time.Now()
	go func() {
		reply, _ := exchange(daemon.socket, []byte(`{"action":"status"`), false)
		stalled <- reply
	}()
	// The daemon serves others while one client stalls.
	if handled, _ := resultOf(t, daemon.send(statusLine(), false)).Get("handled"); handled.StringOf() != "status" {
		t.Errorf("request beside a stalled one handled as %v", handled)
	}
	reply := <-stalled
	elapsed := time.Since(started)
	if reply != "" {
		t.Errorf("stalled request got %q, want no reply", reply)
	}
	if elapsed < requestTimeout-100*time.Millisecond || elapsed > requestTimeout+3*time.Second {
		t.Errorf("stalled connection closed after %v, want about %v", elapsed, requestTimeout)
	}
}

func TestInternalErrorsAnswerCoreOperationFailedWithoutDetail(t *testing.T) {
	const secret = "secret-detail /private/path token=fake-member-token-0001"
	var mode atomic.Int32
	handler := &fakeHandler{member: func(context.Context, wire.Request) (Outcome, error) {
		switch mode.Load() {
		case 0:
			return Outcome{}, errors.New(secret)
		case 1:
			panic(secret)
		case 2:
			return Outcome{}, fmt.Errorf("wrapped: %w", Reject("Unknown conversation in this workspace"))
		default:
			huge := wire.NewObject()
			huge.Set("text", wire.TextValue(strings.Repeat("x", wire.MaxResponseBytes)))
			return Outcome{Result: huge}, nil
		}
	}}
	daemon := startDaemon(t, handler, Options{})
	want := []string{coreOperationFailed, coreOperationFailed, "Unknown conversation in this workspace", coreOperationFailed}
	for index, expected := range want {
		mode.Store(int32(index))
		if got := errorOf(t, daemon.send(statusLine(), false)); got != expected {
			t.Errorf("mode %d: error %q, want %q", index, got, expected)
		}
	}
	logs := daemon.logs.String()
	if strings.Contains(logs, "secret") || strings.Contains(logs, testToken) {
		t.Errorf("log leaks request data:\n%s", logs)
	}
	if !strings.Contains(logs, coreOperationFailed+" (action status)") {
		t.Errorf("log lacks the generic failure line:\n%s", logs)
	}
	if handled := daemon.send(statusLine(), false); errorOf(t, handled) != coreOperationFailed {
		t.Errorf("daemon stopped answering after a panic: %q", handled)
	}
}

func TestConcurrentRequestsRunInParallel(t *testing.T) {
	const parallel = 20
	var arrived sync.WaitGroup
	arrived.Add(parallel)
	allArrived := make(chan struct{})
	go func() {
		arrived.Wait()
		close(allArrived)
	}()
	handler := &fakeHandler{member: func(_ context.Context, request wire.Request) (Outcome, error) {
		arrived.Done()
		// Each request waits for all the others, which only works if they run at once.
		select {
		case <-allArrived:
			return resultNaming(request.Action()), nil
		case <-time.After(5 * time.Second):
			return Outcome{}, Reject("requests were serialised")
		}
	}}
	daemon := startDaemon(t, handler, Options{})
	replies := make(chan string, parallel)
	for range parallel {
		go func() {
			reply, _ := exchange(daemon.socket, []byte(statusLine()), false)
			replies <- reply
		}()
	}
	for range parallel {
		if handled, _ := resultOf(t, <-replies).Get("handled"); handled.StringOf() != "status" {
			t.Errorf("concurrent request handled as %v", handled)
		}
	}
}

func TestReplyIsOneCompactLineWithOrderedResult(t *testing.T) {
	handler := &fakeHandler{member: func(context.Context, wire.Request) (Outcome, error) {
		result := wire.NewObject()
		result.Set("zeta", wire.TextValue("<&> é  "))
		result.Set("alpha", wire.IntValue(1))
		return Outcome{Result: result}, nil
	}}
	daemon := startDaemon(t, handler, Options{})
	reply := daemon.send(statusLine(), false)
	if want := "{\"result\":{\"zeta\":\"<&> é  \",\"alpha\":1}}\n"; reply != want {
		t.Errorf("reply %q, want %q", reply, want)
	}
}

func TestInjectedClockReachesTheHandlerButNotTheTimers(t *testing.T) {
	fixed := time.Date(2001, 2, 3, 4, 5, 6, 0, time.UTC)
	var seen atomic.Int64
	handler := &fakeHandler{}
	daemon := startDaemon(t, &clockRecorder{fakeHandler: handler, seen: &seen}, Options{Now: func() time.Time { return fixed }})
	daemon.send(statusLine(), false)
	if seen.Load() != fixed.Unix() {
		t.Errorf("handler saw %d, want the injected %d", seen.Load(), fixed.Unix())
	}
	started := time.Now()
	daemon.send(readLine(1), false)
	if elapsed := time.Since(started); elapsed < time.Second || elapsed > 2500*time.Millisecond {
		t.Errorf("a 1 s wait took %v under a fixed clock", elapsed)
	}
}

// clockRecorder records the time that Member receives.
type clockRecorder struct {
	*fakeHandler
	seen *atomic.Int64
}

func (recorder *clockRecorder) Member(ctx context.Context, request wire.Request, now time.Time) (Outcome, error) {
	recorder.seen.Store(now.Unix())
	return recorder.fakeHandler.Member(ctx, request, now)
}
