package shim

import (
	"testing"
	"time"

	"github.com/witseko/agentcy/internal/wire"
)

func TestRequestStateIsSignedAndBoundToTheCall(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	signer, err := newRequestStateSigner(func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	arguments := parseLiteral(`{"token": "t", "summary": "s"}`)
	reordered := parseLiteral(`{"summary": "s", "token": "t"}`)
	token := signer.seal("close_conversation", arguments, confirmState)
	if state, err := signer.verify(token, "close_conversation", reordered); err != nil || state != confirmState {
		t.Errorf("verify: %q, %v", state, err)
	}
	if _, err := signer.verify(token, "post_message", arguments); err == nil {
		t.Error("a state for another tool was accepted")
	}
	if _, err := signer.verify(token, "close_conversation", parseLiteral(`{"token": "other"}`)); err == nil {
		t.Error("a state for other arguments was accepted")
	}
	tampered := []byte(token)
	tampered[3] ^= 1
	if _, err := signer.verify(string(tampered), "close_conversation", arguments); err == nil {
		t.Error("a tampered state was accepted")
	}
	other, _ := newRequestStateSigner(func() time.Time { return now })
	if _, err := other.verify(token, "close_conversation", arguments); err == nil {
		t.Error("another process key accepted the state")
	}
	now = now.Add(requestStateTTL)
	if _, err := signer.verify(token, "close_conversation", arguments); err == nil {
		t.Error("an expired state was accepted")
	}
}

// parseLiteral parses a fixed JSON text.
func parseLiteral(text string) wire.Value {
	value, err := wire.Parse([]byte(text))
	if err != nil {
		panic(err)
	}
	return value
}
