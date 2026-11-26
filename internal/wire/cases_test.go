package wire

import (
	"bufio"
	"encoding/json"
	"math/big"
	"os"
	"sort"
	"strings"
	"testing"
)

// memberCase is one line of protocol/member.v1.cases.json. Request and Normalised stay
// raw so that the test feeds the exact bytes to the decoder.
type memberCase struct {
	Action     string          `json:"action"`
	Name       string          `json:"name"`
	Request    json.RawMessage `json:"request"`
	Raw        *string         `json:"raw"`
	Valid      bool            `json:"valid"`
	Normalised json.RawMessage `json:"normalised"`
}

// loadMemberCases reads the case file, one case per line between "[" and "]".
func loadMemberCases(t *testing.T) []memberCase {
	t.Helper()
	file, err := os.Open("../../protocol/member.v1.cases.json")
	if err != nil {
		t.Fatalf("open cases: %v", err)
	}
	defer file.Close()
	var cases []memberCase
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 1<<20), 1<<22)
	for scanner.Scan() {
		line := strings.TrimSuffix(strings.TrimSpace(scanner.Text()), ",")
		if !strings.HasPrefix(line, "{") {
			continue
		}
		var entry memberCase
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatalf("decode case line %q: %v", line, err)
		}
		cases = append(cases, entry)
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("read cases: %v", err)
	}
	return cases
}

func TestDecodeRequestReplaysEveryMemberCase(t *testing.T) {
	cases := loadMemberCases(t)
	if len(cases) < 2000 {
		t.Fatalf("expected the full case file, got %d cases", len(cases))
	}
	for _, entry := range cases {
		input := []byte(entry.Request)
		if entry.Raw != nil {
			input = []byte(*entry.Raw)
		}
		request, err := DecodeRequest(input)
		if (err == nil) != entry.Valid {
			t.Errorf("%s: valid=%v, decoder error=%v", entry.Name, entry.Valid, err)
			continue
		}
		if !entry.Valid {
			continue
		}
		// A given normalised dump pins the field order too; a request echo does not.
		expected, got := entry.Normalised, request.Encode()
		if expected == nil {
			expected, got = entry.Request, sortedKeys(got)
		}
		want, err := Parse(expected)
		if err != nil {
			t.Fatalf("%s: parse expected: %v", entry.Name, err)
		}
		if entry.Normalised == nil {
			want = sortedKeys(want)
		}
		if got.Compact() != want.Compact() {
			t.Errorf("%s:\n got %s\nwant %s", entry.Name, got.Compact(), want.Compact())
		}
	}
}

// sortedKeys returns a copy of value whose objects list their keys in sorted order and
// whose numbers and booleans are in one canonical form, so that the comparison follows
// Python equality, where 1 == 1.0 == True.
func sortedKeys(value Value) Value {
	switch value.Kind {
	case Bool:
		if value.Boolean {
			return Value{Kind: Number, Literal: "1"}
		}
		return Value{Kind: Number, Literal: "0"}
	case Number:
		number, ok := new(big.Rat).SetString(value.Literal)
		if !ok {
			return value
		}
		return Value{Kind: Number, Literal: number.RatString()}
	case Array:
		items := make([]Value, len(value.Items))
		for index, item := range value.Items {
			items[index] = sortedKeys(item)
		}
		return ListValue(items)
	case Object:
		keys := value.Keys()
		sort.Strings(keys)
		sorted := NewObject()
		for _, key := range keys {
			member, _ := value.Get(key)
			sorted.Set(key, sortedKeys(member))
		}
		return sorted
	default:
		return value
	}
}
