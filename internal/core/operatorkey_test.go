package core

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/witseko/agentcy/internal/wire"
)

// caseKey is the key that protocol/operator.v1.cases.json treats as the core's key.
const caseKey = "fake-operator-key-0001"

// urlSafeKey matches secrets.token_urlsafe(32).
var urlSafeKey = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)

// operatorListLine is an operator_list request with key.
func operatorListLine(key string) string {
	return line("version", 1, "key", key, "directory", "/fake/checkout", "action", "operator_list")
}

func TestOperatorKeyIsPrivateRandomAndRewrittenAtEachStart(t *testing.T) {
	dataDir := shortDataDir(t)
	first := startDaemonIn(t, dataDir, &fakeHandler{}, Options{})
	firstKey := first.operatorKeyText()
	info, err := os.Lstat(filepath.Join(dataDir, operatorKeyFileName))
	if err != nil {
		t.Fatal(err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		t.Errorf("operator.key mode %v, want a regular file with 0600", info.Mode())
	}
	if !urlSafeKey.MatchString(firstKey) {
		t.Errorf("key has %d characters or a character outside URL-safe base64", len(firstKey))
	}
	if err := first.stop(); err != nil {
		t.Fatal(err)
	}
	second := startDaemonIn(t, dataDir, &fakeHandler{}, Options{})
	secondKey := second.operatorKeyText()
	if secondKey == firstKey {
		t.Fatal("the key was not rewritten at restart")
	}
	if got := errorOf(t, second.send(operatorListLine(firstKey), false)); got != wire.OperatorKeyRejected {
		t.Errorf("the previous key got %q, want %q", got, wire.OperatorKeyRejected)
	}
	if handled, _ := resultOf(t, second.send(operatorListLine(secondKey), false)).Get("handled"); handled.StringOf() != "operator_list" {
		t.Errorf("the new key was not accepted: %v", handled)
	}
}

func TestOperatorKeyTemporaryFileNeverFollowsASymlink(t *testing.T) {
	dataDir := shortDataDir(t)
	outside := filepath.Join(shortDataDir(t), "target")
	if err := os.WriteFile(outside, []byte("untouched"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dataDir, operatorKeyFileName+".tmp")); err != nil {
		t.Fatal(err)
	}
	daemon := startDaemonIn(t, dataDir, &fakeHandler{}, Options{})
	if data, _ := os.ReadFile(outside); string(data) != "untouched" {
		t.Errorf("the symlink target was written: %q", data)
	}
	info, err := os.Lstat(filepath.Join(dataDir, operatorKeyFileName))
	if err != nil || !info.Mode().IsRegular() {
		t.Errorf("operator.key is not a regular file: %v %v", info, err)
	}
	if !urlSafeKey.MatchString(daemon.operatorKeyText()) {
		t.Error("operator.key does not hold a fresh key")
	}
}

func TestOperatorKeyComparisonCoversFixedLengthDigests(t *testing.T) {
	text, err := newOperatorKeyText()
	if err != nil {
		t.Fatal(err)
	}
	key, err := writeOperatorKey(shortDataDir(t))
	if err != nil {
		t.Fatal(err)
	}
	if key.matches(text) {
		t.Fatal("a different random key matched")
	}
	// Every candidate, whatever its length, is reduced to a 32-byte digest before the
	// constant-time comparison, so no length or prefix shortcut exists to measure.
	for _, candidate := range []string{"", "k", strings.Repeat("k", 43), strings.Repeat("k", 1<<20)} {
		if key.matches(candidate) {
			t.Errorf("candidate of %d bytes matched", len(candidate))
		}
	}
}

func TestOperatorKeyErrorPrecedence(t *testing.T) {
	daemon := startDaemon(t, &fakeHandler{}, Options{})
	key := daemon.operatorKeyText()
	show := func(fields ...any) string {
		base := []any{"version", 1, "directory", "/fake/checkout", "action", "operator_show"}
		return line(append(base, fields...)...)
	}
	cases := []struct {
		name    string
		payload string
		want    string
	}{
		{"wrong key and an invalid conversation", show("key", "wrong", "conversation", "not-an-id"), wire.InvalidArguments},
		{"too long key and an invalid conversation", show("key", strings.Repeat("k", 200), "conversation", "not-an-id"), wire.OperatorKeyRejected},
		{"no key and no directory", line("version", 1, "action", "operator_list"), wire.OperatorKeyRejected},
		{"null key", show("key", nil, "conversation", testRoomID), wire.OperatorKeyRejected},
		{"wrong well-formed key", show("key", "wrong", "conversation", testRoomID), wire.OperatorKeyRejected},
		{"right key and version 2", strings.Replace(show("key", key, "conversation", testRoomID), `"version":1`, `"version":2`, 1), wire.InvalidArguments},
		{"right key", show("key", key, "conversation", testRoomID), ""},
	}
	for _, entry := range cases {
		if got := errorOf(t, daemon.send(entry.payload, false)); got != entry.want {
			t.Errorf("%s: error %q, want %q", entry.name, got, entry.want)
		}
	}
	if logs := daemon.logs.String(); strings.Contains(logs, key) {
		t.Error("the operator key appears in the log")
	}
}

// operatorCase is the part of a case line that the daemon replay needs.
type operatorCase struct {
	Name    string          `json:"name"`
	Request json.RawMessage `json:"request"`
	Raw     *string         `json:"raw"`
	Error   *string         `json:"error"`
}

// loadOperatorCases reads protocol/operator.v1.cases.json, one case per line.
func loadOperatorCases(t *testing.T) []operatorCase {
	t.Helper()
	file, err := os.Open("../../protocol/operator.v1.cases.json")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	var cases []operatorCase
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 1<<20), 1<<22)
	for scanner.Scan() {
		text := strings.TrimSuffix(strings.TrimSpace(scanner.Text()), ",")
		if !strings.HasPrefix(text, "{") {
			continue
		}
		var entry operatorCase
		if err := json.Unmarshal([]byte(text), &entry); err != nil {
			t.Fatalf("decode case: %v", err)
		}
		cases = append(cases, entry)
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return cases
}

// escapedKey spells key as JSON \u escapes, the form one case uses.
func escapedKey(key string) string {
	var builder strings.Builder
	for _, character := range key {
		fmt.Fprintf(&builder, `\u%04x`, character)
	}
	return builder.String()
}

func TestDaemonAnswersEveryOperatorCaseAsThePythonCore(t *testing.T) {
	daemon := startDaemon(t, &fakeHandler{}, Options{})
	key := daemon.operatorKeyText()
	cases := loadOperatorCases(t)
	if len(cases) < 2000 {
		t.Fatalf("expected the full case file, got %d cases", len(cases))
	}
	for _, entry := range cases {
		payload := string(entry.Request)
		if entry.Raw != nil {
			payload = *entry.Raw
		}
		// The cases name the core's key caseKey; this daemon has its own random key.
		payload = strings.ReplaceAll(payload, caseKey, key)
		payload = strings.ReplaceAll(payload, escapedKey(caseKey), escapedKey(key))
		reply := daemon.send(payload+"\n", false)
		want := ""
		if entry.Error != nil {
			want = *entry.Error
		}
		if got := errorOf(t, reply); got != want {
			t.Errorf("%s: error %q, want %q", entry.Name, got, want)
		}
	}
	if logs := daemon.logs.String(); strings.Contains(logs, key) {
		t.Error("the operator key appears in the log")
	}
}
