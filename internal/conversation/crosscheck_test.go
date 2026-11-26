package conversation

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/witseko/agentcy/internal/paths"
)

// trickyStrings mix compatibility forms, combining marks, look-alike letters, non-ASCII
// digits and numbers, and FTS5 punctuation.
var trickyStrings = []string{
	"Human", "Hυman", "Ｈｕｍａｎ", "Straße", "ǅungla", "été", "किताब", "٣٤٥ digits",
	"Ⅻ ⅷ ½ ²", "test_case", `a"b OR c* NEAR(d)`, "ﬁle ﬂow", "İstanbul", "ΣΊΣΥΦΟΣ", "日本語テキスト",
	"\U0001d400bold", "x​y", "tab\tand space", "", "!!!", "Worker 2", "ʰmodifier", "〇一二",
}

// pythonCrosscheck computes name_skeleton and the search words of each argument, issues a
// join code for argv[1], and checks the Go code given in argv[2].
const pythonCrosscheck = `
import json, sys
from agentcy.conversations import repository, search
workspace, go_code, strings = sys.argv[1], sys.argv[2], json.loads(sys.argv[3])
code, fields = repository.new_join_code(workspace)
tagged = repository.JOIN_CODE_PATTERN.fullmatch(go_code)
print(json.dumps({
    "skeletons": [repository.name_skeleton(text) for text in strings],
    "words": [search.WORD.findall(text) for text in strings],
    "code": code,
    "code_digest": fields["invite_hash"],
    "go_code_matches": bool(tagged) and tagged[2] == repository.join_workspace_tag(workspace, tagged[1]),
    "go_code_digest": repository.digest(go_code),
}))
`

// pythonResult is what pythonCrosscheck prints.
type pythonResult struct {
	Skeletons     []string   `json:"skeletons"`
	Words         [][]string `json:"words"`
	Code          string     `json:"code"`
	CodeDigest    string     `json:"code_digest"`
	GoCodeMatches bool       `json:"go_code_matches"`
	GoCodeDigest  string     `json:"go_code_digest"`
}

// TestTextAndCodesMatchPython compares name skeletons and search words with Python, and
// redeems a code issued by each implementation with the other. It skips without uv.
func TestTextAndCodesMatchPython(t *testing.T) {
	goCode := newJoinCode(testWorkspace, startUnix)
	python := runPythonCrosscheck(t, goCode.code)
	for index, input := range trickyStrings {
		if got := nameSkeleton(input); got != python.Skeletons[index] {
			t.Errorf("nameSkeleton(%q) = %q, Python %q", input, got, python.Skeletons[index])
		}
		got := searchTerms(input)
		if len(got) != len(python.Words[index]) {
			t.Errorf("searchTerms(%q) = %q, Python %q", input, got, python.Words[index])
			continue
		}
		for position := range got {
			if got[position] != python.Words[index][position] {
				t.Errorf("searchTerms(%q) = %q, Python %q", input, got, python.Words[index])
			}
		}
	}
	if tagged, matches := codeTagMatches(python.Code, testWorkspace); !tagged || !matches {
		t.Error("a code issued by Python does not match its workspace in Go")
	}
	if digest(python.Code) != python.CodeDigest {
		t.Error("Go stores another digest for a code issued by Python")
	}
	if !python.GoCodeMatches || python.GoCodeDigest != goCode.hash {
		t.Error("Python does not accept a code issued by Go")
	}
}

// runPythonCrosscheck runs pythonCrosscheck through uv, or skips the test when uv or the
// project environment is unavailable.
func runPythonCrosscheck(t *testing.T, goCode string) pythonResult {
	t.Helper()
	pathValue, pathSet := os.LookupEnv("PATH")
	uv := paths.FindOnPath("uv", pathValue, pathSet)
	if uv == "" {
		t.Skip("uv is unavailable")
	}
	repository, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(trickyStrings)
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(uv, "run", "--locked", "--project", repository, "python", "-I", "-c", pythonCrosscheck, testWorkspace, goCode, string(encoded))
	command.Env = append(os.Environ(), paths.HomeVariable+"="+t.TempDir())
	output, err := command.Output()
	if err != nil {
		if exit, ok := err.(*exec.ExitError); ok {
			t.Fatalf("Python failed: %v\n%s", err, exit.Stderr)
		}
		t.Skipf("Python unavailable: %v", err)
	}
	var result pythonResult
	if err := json.Unmarshal(output, &result); err != nil {
		t.Fatalf("parse %q: %v", output, err)
	}
	return result
}
