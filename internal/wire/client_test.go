package wire

import (
	"bufio"
	"errors"
	"net"
	"strings"
	"syscall"
	"testing"
)

// fakeCore answers each connection with the next scripted response line, or closes it
// without answering when the line is empty.
type fakeCore struct {
	responses []string
	requests  []string
}

// connect returns one end of an in-memory connection and serves the other end.
func (core *fakeCore) connect() (net.Conn, error) {
	client, server := net.Pipe()
	response := ""
	if len(core.responses) > 0 {
		response, core.responses = core.responses[0], core.responses[1:]
	}
	go func() {
		defer server.Close()
		line, err := bufio.NewReader(server).ReadString('\n')
		if err != nil {
			return
		}
		core.requests = append(core.requests, line)
		if response != "" {
			_, _ = server.Write([]byte(response))
		}
	}()
	return client, nil
}

func validRead() *Read {
	return &Read{Workspace: strings.Repeat("a", 64), Token: "member-token"}
}

func TestExchangeRetriesOnceForIdempotentActions(t *testing.T) {
	core := &fakeCore{responses: []string{"", `{"result": {"ok": true}}` + "\n"}}
	client := &Client{Connect: core.connect}
	result, err := client.Exchange(validRead())
	if err != nil {
		t.Fatalf("Exchange: %v", err)
	}
	if result.Compact() != `{"ok":true}` || len(core.requests) != 2 {
		t.Errorf("result %s after %d requests", result.Compact(), len(core.requests))
	}
}

func TestExchangeNeverRetriesCreate(t *testing.T) {
	core := &fakeCore{responses: []string{"", `{"result": {}}` + "\n"}}
	client := &Client{Connect: core.connect}
	create := &Create{Workspace: strings.Repeat("a", 64), Brief: "Test", Initiator: "Lead", Invitees: []Slot{{Name: "Worker"}}}
	if _, err := client.Exchange(create); !errors.Is(err, ErrIncompleteResponse) {
		t.Errorf("err = %v, want ErrIncompleteResponse", err)
	}
	if len(core.requests) != 1 {
		t.Errorf("create was sent %d times", len(core.requests))
	}
}

func TestTransientDisconnectsAreRetried(t *testing.T) {
	for _, err := range []error{syscall.EPIPE, syscall.ECONNRESET, ErrIncompleteResponse} {
		if !isTransientDisconnect(err) {
			t.Errorf("%v is not retried", err)
		}
	}
	if isTransientDisconnect(ErrInvalidResponse) {
		t.Error("an invalid response is retried")
	}
}

func TestCoreRejectingAValidRequestReportsAVersionMismatch(t *testing.T) {
	core := &fakeCore{responses: []string{`{"error": "Invalid bridge arguments"}` + "\n"}}
	client := &Client{Connect: core.connect}
	join := &Join{Workspace: strings.Repeat("a", 64), Code: "abcdefghijklmnopqrstuv.0123456789ab"}
	_, err := client.Exchange(join)
	var rejection *CoreError
	if !errors.As(err, &rejection) {
		t.Fatalf("err = %v, want a CoreError", err)
	}
	if !strings.HasPrefix(err.Error(), InvalidArguments) || !strings.Contains(err.Error(), "Stop the process running agentcy.mcp.core") {
		t.Errorf("error text %q", err.Error())
	}
	if strings.Contains(err.Error(), join.Code) {
		t.Error("the error repeats the join code")
	}
}

func TestCoreErrorPassesThrough(t *testing.T) {
	core := &fakeCore{responses: []string{`{"error": "Conversation is closed"}` + "\n"}}
	client := &Client{Connect: core.connect}
	if _, err := client.Exchange(validRead()); err == nil || err.Error() != "Conversation is closed" {
		t.Errorf("err = %v", err)
	}
}

func TestRequestLineIsOneCompactJSONLine(t *testing.T) {
	core := &fakeCore{responses: []string{`{"result": {}}` + "\n"}}
	client := &Client{Connect: core.connect}
	post := &Post{Workspace: strings.Repeat("a", 64), Token: "t", SubmissionID: "s", Body: "é <b> &  ", Kind: "note"}
	if _, err := client.Exchange(post); err != nil {
		t.Fatal(err)
	}
	want := `{"submission_id":"s","body":"é <b> & ` + " " + `","kind":"note","to":null,"reply_to":null,"due_minutes":null,"workspace":"` + strings.Repeat("a", 64) + `","token":"t","action":"post"}` + "\n"
	if core.requests[0] != want {
		t.Errorf("request line\n got %q\nwant %q", core.requests[0], want)
	}
}

func TestSizeCapsAreEnforcedWithABoundedReader(t *testing.T) {
	long := `{"result": "` + strings.Repeat("x", MaxResponseBytes) + `"}` + "\n"
	if _, err := readBoundedLine(bufio.NewReader(strings.NewReader(long)), MaxResponseBytes); !errors.Is(err, ErrIncompleteResponse) {
		t.Errorf("oversized line: err = %v", err)
	}
	if _, err := readBoundedLine(bufio.NewReader(strings.NewReader(`{"result": 1}`)), MaxResponseBytes); !errors.Is(err, ErrIncompleteResponse) {
		t.Errorf("line without newline: err = %v", err)
	}
	exact := strings.Repeat("x", MaxResponseBytes-1) + "\n"
	if line, err := readBoundedLine(bufio.NewReader(strings.NewReader(exact)), MaxResponseBytes); err != nil || len(line) != MaxResponseBytes {
		t.Errorf("line at the cap: %d bytes, err = %v", len(line), err)
	}
	if MaxRequestBytes != 128*1024 {
		t.Errorf("MaxRequestBytes = %d", MaxRequestBytes)
	}
}
