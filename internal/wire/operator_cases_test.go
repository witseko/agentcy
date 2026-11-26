package wire

import (
	"encoding/json"
	"errors"
	"os"
	"testing"
)

// operatorCaseKey is the key that every operator case treats as the core's key.
const operatorCaseKey = "fake-operator-key-0001"

// answerOf maps a decode error to the text the core sends.
func answerOf(err error) string {
	if errors.Is(err, ErrOperatorKeyRejected) {
		return OperatorKeyRejected
	}
	return InvalidArguments
}

func TestDecodeIncomingReplaysEveryOperatorCase(t *testing.T) {
	cases := loadCases(t, "../../protocol/operator.v1.cases.json")
	if len(cases) < 2000 {
		t.Fatalf("expected the full case file, got %d cases", len(cases))
	}
	for _, entry := range cases {
		incoming, err := DecodeIncoming(entry.input())
		if (err == nil) != entry.Valid {
			t.Errorf("%s: valid=%v, decoder error=%v", entry.Name, entry.Valid, err)
			continue
		}
		if !entry.Valid {
			if entry.Error == nil || answerOf(err) != *entry.Error {
				t.Errorf("%s: answer %q, want %v", entry.Name, answerOf(err), entry.Error)
			}
			continue
		}
		if incoming.Operator == nil || incoming.Member != nil {
			t.Errorf("%s: decoded %+v, want one operator request", entry.Name, incoming)
			continue
		}
		keyMatches := incoming.Operator.OperatorKey() == operatorCaseKey
		if keyMatches != (entry.Error == nil) || (entry.Error != nil && *entry.Error != OperatorKeyRejected) {
			t.Errorf("%s: key match %v, but the core answers %v", entry.Name, keyMatches, entry.Error)
		}
		checkNormalised(t, entry, Encode(incoming.Operator))
	}
}

func TestDecodeIncomingAgreesWithDecodeRequestOnMemberCases(t *testing.T) {
	for _, entry := range loadMemberCases(t) {
		incoming, err := DecodeIncoming(entry.input())
		if (err == nil) != entry.Valid {
			t.Errorf("%s: valid=%v, decoder error=%v", entry.Name, entry.Valid, err)
			continue
		}
		if err != nil && answerOf(err) != InvalidArguments {
			t.Errorf("%s: member rejection answers %q", entry.Name, answerOf(err))
		}
		if err == nil && (incoming.Member == nil || incoming.Operator != nil) {
			t.Errorf("%s: decoded %+v, want one member request", entry.Name, incoming)
		}
	}
}

func TestDecodeIncomingAcceptsEveryOperatorExample(t *testing.T) {
	data, err := os.ReadFile("../../protocol/operator.v1.examples.json")
	if err != nil {
		t.Fatal(err)
	}
	var examples []struct {
		Action  string          `json:"action"`
		Request json.RawMessage `json:"request"`
	}
	if err := json.Unmarshal(data, &examples); err != nil {
		t.Fatal(err)
	}
	actions := map[string]bool{}
	for _, example := range examples {
		incoming, err := DecodeIncoming(example.Request)
		if err != nil || incoming.Operator == nil || incoming.Operator.Action() != example.Action {
			t.Errorf("%s: decoded %+v, error %v", example.Action, incoming, err)
			continue
		}
		actions[example.Action] = true
	}
	if len(actions) != len(operatorRequests) {
		t.Errorf("examples cover %d actions, decoders know %d", len(actions), len(operatorRequests))
	}
}

func TestOperatorEncodeRoundTrips(t *testing.T) {
	for _, entry := range loadCases(t, "../../protocol/operator.v1.cases.json") {
		if !entry.Valid {
			continue
		}
		first, err := DecodeIncoming(entry.input())
		if err != nil {
			t.Fatalf("%s: %v", entry.Name, err)
		}
		encoded := Encode(first.Operator).Compact()
		second, err := DecodeIncoming([]byte(encoded))
		if err != nil || Encode(second.Operator).Compact() != encoded {
			t.Errorf("%s: re-decoding %s gave error %v", entry.Name, encoded, err)
		}
	}
}
