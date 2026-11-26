//go:build conformance

// Package conformance drives an agentcy core daemon over its Unix socket and compares the
// normalised response of every request with a golden file recorded from the Python core.
//
// Run it against any daemon:
//
//	AGENTCY_CORE_CMD="uv run python -I -m agentcy.mcp.core" go test -tags conformance ./conformance/...
//
// Add -update to record the goldens again. Each test starts its own daemon with a fresh
// AGENTCY_HOME and a temp workspace, so tests never share state or touch ~/.agentcy.
//
// To add a scenario, copy a test function: start with newScenario (or newGitScenario),
// send requests with s.member, s.post and s.operator, read values from a reply with
// text and number, then run with -update and read the new golden before committing it.
package conformance

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var update = flag.Bool("update", false, "record the golden files from the daemon under test")

// fields is the body of one request. Keys are JSON field names.
type fields map[string]any

// scenario is one test's daemon, workspace and transcript of normalised steps.
type scenario struct {
	t      *testing.T
	daemon *daemon
	// directory is the checkout path sent in operator requests.
	directory string
	// workspace is the key that a stdio shim derives from directory.
	workspace   string
	operatorKey string
	normalizer  *normalizer
	transcript  strings.Builder
	steps       int
	submissions int
}

// newScenario starts a daemon for a plain (not git) workspace directory.
func newScenario(t *testing.T) *scenario {
	t.Helper()
	s := startScenario(t)
	s.useWorkspace(plainWorkspace(t, s.normalizer, "<checkout>"))
	return s
}

// newGitScenario starts a daemon for a workspace that is a git repository.
func newGitScenario(t *testing.T) *scenario {
	t.Helper()
	s := startScenario(t)
	s.useWorkspace(gitWorkspace(t, s.normalizer))
	return s
}

func startScenario(t *testing.T) *scenario {
	t.Helper()
	normalizer := newNormalizer(time.Now().Unix())
	server := startDaemon(t)
	s := &scenario{t: t, daemon: server, normalizer: normalizer}
	s.operatorKey = server.operatorKey(t)
	normalizer.addLiteral(s.operatorKey, "<operator-key>")
	normalizer.addLiteral(server.home, "<home>")
	// Registered after startDaemon, so it runs before the daemon stops.
	t.Cleanup(s.checkGolden)
	return s
}

// checkout is a workspace directory and the key the daemon derives from it.
type checkout struct {
	directory string
	workspace string
}

func (s *scenario) useWorkspace(workspace checkout) {
	s.directory = workspace.directory
	s.workspace = workspace.workspace
}

// plainWorkspace makes a temp directory. Its key is the SHA-256 of its resolved path.
func plainWorkspace(t *testing.T, normalizer *normalizer, name string) checkout {
	t.Helper()
	directory := shortTempDir(t, "agentcy-work-")
	resolved := resolvePath(t, directory)
	normalizer.addLiteral(resolved, name)
	normalizer.addLiteral(directory, name)
	return checkout{directory: directory, workspace: sha256Hex(resolved)}
}

// gitWorkspace makes a temp git repository. Its key is the SHA-256 of the resolved path
// of its common git directory, so every linked worktree shares one store.
func gitWorkspace(t *testing.T, normalizer *normalizer) checkout {
	t.Helper()
	parent := shortTempDir(t, "agentcy-repo-")
	directory := filepath.Join(parent, "main")
	runGit(t, parent, "init", "-q", directory)
	resolved := resolvePath(t, directory)
	normalizer.addLiteral(resolvePath(t, parent), "<repository-parent>")
	normalizer.addLiteral(parent, "<repository-parent>")
	return checkout{directory: directory, workspace: sha256Hex(filepath.Join(resolved, ".git"))}
}

// runGit runs git without the user's configuration, so hooks and templates cannot interfere.
func runGit(t *testing.T, directory string, arguments ...string) {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", directory}, arguments...)...)
	command.Env = []string{"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1"}
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "GIT_") {
			command.Env = append(command.Env, entry)
		}
	}
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", arguments, err, output)
	}
}

func resolvePath(t *testing.T, path string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatalf("resolve %s: %v", path, err)
	}
	return resolved
}

func sha256Hex(text string) string {
	digest := sha256.Sum256([]byte(text))
	return hex.EncodeToString(digest[:])
}

// member sends a member request. It adds the action and the scenario's workspace key;
// request fields override them.
func (s *scenario) member(label, action string, request fields) reply {
	s.t.Helper()
	full := fields{"action": action, "workspace": s.workspace}
	for key, value := range request {
		full[key] = value
	}
	return s.send(label, full)
}

// post sends a post as the token's owner with the next submission ID ("submission-N").
// Pass "submission_id" in request to choose one, for example to test a retry.
func (s *scenario) post(label, token string, request fields) reply {
	s.t.Helper()
	s.submissions++
	full := fields{"token": token, "submission_id": fmt.Sprintf("submission-%d", s.submissions)}
	for key, value := range request {
		full[key] = value
	}
	return s.member(label, "post", full)
}

// operatorRequest builds an operator request with version 1, the operator key and the
// checkout directory. Tests that need a broken request change the map before sending it.
func (s *scenario) operatorRequest(action string, request fields) fields {
	full := fields{"version": 1, "key": s.operatorKey, "action": action, "directory": s.directory}
	for key, value := range request {
		full[key] = value
	}
	return full
}

// operator sends an operator request built by operatorRequest.
func (s *scenario) operator(label, action string, request fields) reply {
	s.t.Helper()
	return s.send(label, s.operatorRequest(action, request))
}

// send encodes a request as one JSON line, sends it and records the step.
func (s *scenario) send(label string, request fields) reply {
	s.t.Helper()
	line, err := encodeRequest(request)
	if err != nil {
		s.t.Fatalf("%s: encode request: %v", label, err)
	}
	decoded, err := decodeOrdered(line)
	if err != nil {
		s.t.Fatalf("%s: decode own request: %v", label, err)
	}
	shown := formatJSON(shortenLongStrings(s.normalizer.value(decoded, "")), false)
	return s.exchange(label, shown, line, false)
}

// sendRaw sends exact bytes, for framing tests. The golden shows the description instead
// of the bytes. With closeWrite the client half-closes after the bytes.
func (s *scenario) sendRaw(label, description string, payload []byte, closeWrite bool) reply {
	s.t.Helper()
	return s.exchange(label, "<raw: "+description+">", payload, closeWrite)
}

// exchange performs one request and appends the normalised step to the transcript.
func (s *scenario) exchange(label, shownRequest string, payload []byte, closeWrite bool) reply {
	s.t.Helper()
	s.steps++
	line, err := exchange(s.daemon.socket, payload, closeWrite)
	if err != nil {
		s.t.Fatalf("step %d (%s): %v", s.steps, label, err)
	}
	fmt.Fprintf(&s.transcript, "== %d. %s\n-> %s\n", s.steps, label, shownRequest)
	if line == "" {
		s.transcript.WriteString("<- <no response>\n\n")
		return reply{s: s, label: label}
	}
	decoded, err := decodeOrdered([]byte(line))
	if err != nil {
		s.t.Fatalf("step %d (%s): reply is not JSON: %v", s.steps, label, err)
	}
	fmt.Fprintf(&s.transcript, "<- %s\n\n", formatJSON(s.normalizer.value(decoded, ""), true))
	return newReply(s, label, line)
}

// checkGolden compares the transcript with testdata/<TestName>.golden, or writes it with -update.
func (s *scenario) checkGolden() {
	if s.t.Failed() {
		return
	}
	path := filepath.Join("testdata", s.t.Name()+".golden")
	got := s.transcript.String()
	if *update {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			s.t.Errorf("write golden: %v", err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		s.t.Errorf("read golden %s: %v (run with -update to record it)", path, err)
		return
	}
	if string(want) != got {
		s.t.Errorf("transcript differs from %s:\n%s", path, firstDifference(string(want), got))
	}
}

// firstDifference describes the first line where two transcripts differ, with the step header.
func firstDifference(want, got string) string {
	wantLines := strings.Split(want, "\n")
	gotLines := strings.Split(got, "\n")
	header := ""
	for index := 0; index < len(wantLines) || index < len(gotLines); index++ {
		wantLine := lineAt(wantLines, index)
		gotLine := lineAt(gotLines, index)
		if strings.HasPrefix(wantLine, "== ") {
			header = wantLine
		}
		if wantLine != gotLine {
			return fmt.Sprintf("line %d, in step %q\nwant: %s\ngot:  %s", index+1, header, wantLine, gotLine)
		}
	}
	return "no line differs"
}

func lineAt(lines []string, index int) string {
	if index < len(lines) {
		return lines[index]
	}
	return "<end of transcript>"
}

// reply is one decoded daemon reply. Use text and number to read values from its result.
type reply struct {
	s         *scenario
	label     string
	result    any
	errorText string
}

func newReply(s *scenario, label, line string) reply {
	var envelope struct {
		Result any    `json:"result"`
		Error  string `json:"error"`
	}
	if err := json.Unmarshal([]byte(line), &envelope); err != nil {
		s.t.Fatalf("%s: decode reply: %v", label, err)
	}
	return reply{s: s, label: label, result: envelope.Result, errorText: envelope.Error}
}

// lookup follows a path of object keys (string) and array indexes (int) inside the result.
func (r reply) lookup(path ...any) any {
	r.s.t.Helper()
	if r.errorText != "" || r.result == nil {
		r.s.t.Fatalf("%s: no result to read %v from (error %q)", r.label, path, r.errorText)
	}
	current := r.result
	for _, step := range path {
		current = lookupStep(current, step)
		if current == nil {
			r.s.t.Fatalf("%s: result has no value at %v", r.label, path)
		}
	}
	return current
}

func lookupStep(value any, step any) any {
	switch key := step.(type) {
	case string:
		object, isObject := value.(map[string]any)
		if isObject {
			return object[key]
		}
	case int:
		array, isArray := value.([]any)
		if isArray && key < len(array) {
			return array[key]
		}
	}
	return nil
}

// text reads a string from the result, for example created.text("invitations", "Worker").
func (r reply) text(path ...any) string {
	r.s.t.Helper()
	value, isString := r.lookup(path...).(string)
	if !isString {
		r.s.t.Fatalf("%s: value at %v is not a string", r.label, path)
	}
	return value
}

// number reads an integer from the result, for example posted.number("message_id").
func (r reply) number(path ...any) int {
	r.s.t.Helper()
	value, isNumber := r.lookup(path...).(float64)
	if !isNumber {
		r.s.t.Fatalf("%s: value at %v is not a number", r.label, path)
	}
	return int(value)
}

// invitationCode reads the join code of one slot from an operator_create result.
func (r reply) invitationCode(participant string) string {
	r.s.t.Helper()
	invitations, _ := r.lookup("invitations").([]any)
	for _, item := range invitations {
		invitation, _ := item.(map[string]any)
		if invitation["participant"] == participant {
			code, _ := invitation["code"].(string)
			return code
		}
	}
	r.s.t.Fatalf("%s: no invitation for %s", r.label, participant)
	return ""
}
