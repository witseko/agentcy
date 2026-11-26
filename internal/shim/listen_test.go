package shim

import (
	"errors"
	"slices"
	"testing"

	"github.com/witseko/agentcy/internal/wire"
)

// Fixture page and status of a worker's waiting read.
const (
	listenPage   = `{"messages": [{"id": 5, "label": "Lead", "body": "Fake note.", "kind": "note"}], "more": false, "cursor": 5, "wake": true}`
	listenStatus = `{"open": true, "participant": "Helper", "members": {"Lead": {"role": "lead", "state": "replied"},
		"Helper": {"role": "worker", "state": "joined"}}}`
	listenToken = "fake-member-token-0001"
)

// newCore returns a fake core that answers a read and a status, and a listen unless
// listenFailure is set, in which case a listen fails with it.
func newCore(listenFailure error) *fakeCore {
	core := &fakeCore{answers: map[string]string{
		"read":   listenPage,
		"status": listenStatus,
		"listen": `{"page": ` + listenPage + `, "status": ` + listenStatus + `}`,
	}}
	if listenFailure != nil {
		core.failures = map[string]error{"listen": listenFailure}
	}
	return core
}

// oldCoreRejection is the answer of a core that does not know an action, as the client
// decodes it.
var oldCoreRejection = &wire.CoreError{Message: wire.InvalidArguments + ". The running core is older."}

// waitOnce runs one waiting read through readMember and fails the test on an error.
func waitOnce(t *testing.T, core *fakeCore, state *listenState) wire.Value {
	t.Helper()
	page, err := readMember(core, state, testWorkspace, listenToken, 4, 30)
	if err != nil {
		t.Fatal(err)
	}
	return page
}

// Port plan fix L2: against a core with listen, a waiting read is one exchange, and the
// tool result equals the one built from a read and a status.
func TestWaitingReadIsOneListenExchange(t *testing.T) {
	current := newCore(nil)
	page := waitOnce(t, current, newListenState(true))
	if got := current.actions(); !slices.Equal(got, []string{"listen"}) {
		t.Errorf("actions %v, want one listen", got)
	}
	listen, _ := current.requests[0].(*wire.Listen)
	if listen == nil || listen.After != 4 || listen.WaitSeconds != 30 || listen.Token != listenToken {
		t.Errorf("listen request %+v", current.requests[0])
	}
	old := newCore(oldCoreRejection)
	oldPage := waitOnce(t, old, newListenState(true))
	if page.Compact() != oldPage.Compact() {
		t.Errorf("listen result differs from read and status:\nlisten: %s\nold:    %s", page.Compact(), oldPage.Compact())
	}
}

// A core that predates listen answers Invalid bridge arguments: the shim sends a read and
// a status instead, and keeps doing so without trying listen again.
func TestWaitingReadFallsBackOnceForAnOldCore(t *testing.T) {
	old := newCore(oldCoreRejection)
	state := newListenState(true)
	waitOnce(t, old, state)
	waitOnce(t, old, state)
	want := []string{"listen", "read", "status", "read", "status"}
	if got := old.actions(); !slices.Equal(got, want) {
		t.Errorf("actions %v, want %v", got, want)
	}
	read, _ := old.requests[1].(*wire.Read)
	if read == nil || read.After != 4 || read.WaitSeconds != 30 {
		t.Errorf("fallback read %+v, want the waiting read", old.requests[1])
	}
}

// Any other failure of a listen is the tool's error and does not switch to the old
// requests.
func TestListenFailureOtherThanAnOldCoreIsReturned(t *testing.T) {
	failure := &wire.CoreError{Message: "Invalid participant token"}
	core := newCore(failure)
	state := newListenState(true)
	if _, err := readMember(core, state, testWorkspace, listenToken, 4, 30); !errors.Is(err, failure) {
		t.Errorf("err = %v, want the core's rejection", err)
	}
	if state.usesOldCore() {
		t.Error("a rejection other than Invalid bridge arguments marked the core as old")
	}
}

// A read without a wait stays a plain read.
func TestReadWithoutWaitIsAPlainRead(t *testing.T) {
	core := newCore(nil)
	if _, err := readMember(core, newListenState(true), testWorkspace, listenToken, 4, 0); err != nil {
		t.Fatal(err)
	}
	if got := core.actions(); !slices.Equal(got, []string{"read"}) {
		t.Errorf("actions %v, want one read", got)
	}
}
