package shim

import (
	"bytes"
	"fmt"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/witseko/agentcy/internal/paths"
	"github.com/witseko/agentcy/internal/progress"
	"github.com/witseko/agentcy/internal/wire"
)

// pythonFinishWait replays every case twice through the Python finish_wait with the same
// quiet-wait and team memory, as two consecutive waiting reads.
const pythonFinishWait = `
import json, sys
from agentcy.mcp import server
out = []
for case in json.load(sys.stdin):
    waits = {"d": case["prior"]} if case["prior"] else {}
    teams = {}
    pages = []
    for _ in range(2):
        page = json.loads(json.dumps(case["page"]))
        status = json.loads(json.dumps(case["status"]))
        pages.append(server.finish_wait(page, status, waits, "d", last_teams=teams, unread_other=case["unread"]))
    out.append(pages)
print(json.dumps(out, ensure_ascii=False))
`

// TestFinishWaitMatchesPython compares Go and Python guidance on random rooms. It runs
// the Python shim's finish_wait through uv and skips when uv is unavailable.
func TestFinishWaitMatchesPython(t *testing.T) {
	pathValue, pathSet := os.LookupEnv("PATH")
	uv := paths.FindOnPath("uv", pathValue, pathSet)
	if uv == "" {
		t.Skip("uv is unavailable")
	}
	generator := rand.New(rand.NewPCG(7, 11))
	cases := make([]wire.Value, 0, 600)
	for range 600 {
		cases = append(cases, randomCase(generator))
	}
	repository, _ := filepath.Abs("../..")
	command := exec.Command(uv, "run", "--locked", "--project", repository, "python", "-c", pythonFinishWait)
	command.Env = os.Environ()
	command.Stdin = bytes.NewReader([]byte(wire.ListValue(cases).Compact()))
	var errors bytes.Buffer
	command.Stderr = &errors
	output, err := command.Output()
	if err != nil {
		t.Fatalf("Python finish_wait failed: %v\n%s", err, errors.String())
	}
	expected, err := wire.Parse(output)
	if err != nil {
		t.Fatalf("parse Python output: %v", err)
	}
	for index, testCase := range cases {
		got := goFinishTwice(testCase)
		for round := range 2 {
			want := expected.Items[index].Items[round].Compact()
			if got[round] != want {
				t.Errorf("case %d round %d\ninput %s\n  go %s\n  py %s", index, round, testCase.Compact(), got[round], want)
			}
		}
	}
}

// goFinishTwice runs the Go finishWait twice on one case with shared state.
func goFinishTwice(testCase wire.Value) [2]string {
	state := newListenState(true)
	prior, _ := testCase.Get("prior")
	if count, _ := prior.IntOf(); count > 0 {
		state.silentWaits["d"] = int(count)
	}
	unread, _ := testCase.Get("unread")
	unreadCount, _ := unread.IntOf()
	var pages [2]string
	for round := range 2 {
		page, _ := testCase.Get("page")
		statusValue, _ := testCase.Get("status")
		statusValue = statusValue.Copy()
		result := finishWait(page.Copy(), progress.StatusFromValue(statusValue), statusValue, state, "d", int(unreadCount))
		pages[round] = result.Compact()
	}
	return pages
}

// pick returns a random element.
func pick(generator *rand.Rand, choices []string) string {
	return choices[generator.IntN(len(choices))]
}

// randomCase builds one random room status, page, and quiet-wait history.
func randomCase(generator *rand.Rand) wire.Value {
	names := []string{"Lead", "Ana", "Bo", "Cy", "Di"}[:2+generator.IntN(4)]
	me := names[generator.IntN(len(names))]
	members := wire.NewObject()
	idleOffsets := generator.Perm(len(names))
	nextTaskID := int64(1)
	for index, name := range names {
		role := pick(generator, []string{"lead", "co-lead", "worker", "worker", "human"})
		if name == me && role == "human" {
			role = "worker"
		}
		member := wire.NewObject()
		member.Set("role", wire.TextValue(role))
		member.Set("state", wire.TextValue(pick(generator, []string{"invited", "joined", "replied", "done", "blocked", "retired"})))
		if generator.IntN(3) == 0 {
			member.Set("launch_state", wire.TextValue(pick(generator, []string{"queued", "starting", "running", "finished", "failed", "cancelled", "timed_out", "unknown"})))
		}
		if generator.IntN(2) == 0 {
			member.Set("idle_seconds", wire.IntValue(int64(idleOffsets[index]*70+generator.IntN(60))))
		}
		var tasks []wire.Value
		for range generator.IntN(3) {
			task := wire.NewObject()
			task.Set("id", wire.IntValue(nextTaskID))
			nextTaskID++
			if generator.IntN(3) == 0 {
				task.Set("blocked", wire.BoolValue(true))
			}
			if generator.IntN(4) == 0 {
				task.Set("overdue", wire.BoolValue(true))
			}
			tasks = append(tasks, task)
		}
		if len(tasks) > 0 {
			member.Set("open_tasks", wire.ListValue(tasks))
		}
		if generator.IntN(4) == 0 {
			usage := wire.NewObject()
			usage.Set("model", wire.TextValue("m"))
			usage.Set("conversation_tokens", wire.IntValue(int64(generator.IntN(5000))))
			usage.Set("budget", wire.IntValue(9000))
			member.Set("usage", usage)
		}
		members.Set(name, member)
	}
	status := wire.NewObject()
	status.Set("open", wire.BoolValue(generator.IntN(10) > 0))
	status.Set("participant", wire.TextValue(me))
	status.Set("members", members)
	switch generator.IntN(4) {
	case 0:
		status.Set("round", wire.TextValue("sealed"))
		status.Set("proposal_submitted", wire.BoolValue(generator.IntN(2) == 0))
	case 1:
		status.Set("round", wire.TextValue("critique"))
	}
	if generator.IntN(4) == 0 {
		status.Set("open_invite", wire.BoolValue(true))
	}
	switch generator.IntN(3) {
	case 0:
		status.Set("token_balance", parseLiteral(`{"balanced": true, "imbalance_gap_percent": 3.5}`))
	case 1:
		status.Set("token_balance", parseLiteral(`{"balanced": false, "imbalance_gap_percent": 40.0, "recommendation": "Give the next task to Bo."}`))
	}
	testCase := wire.NewObject()
	testCase.Set("status", status)
	testCase.Set("page", randomPage(generator, names, me, nextTaskID))
	testCase.Set("prior", wire.IntValue(int64([]int{0, 0, 1, 2, 3, 7, 79, 80}[generator.IntN(8)])))
	testCase.Set("unread", wire.IntValue(int64(generator.IntN(3))))
	return testCase
}

// randomPage builds a page with zero to three messages.
func randomPage(generator *rand.Rand, names []string, me string, maxTaskID int64) wire.Value {
	var messages []wire.Value
	var blockers []wire.Value
	for index := range generator.IntN(4) {
		message := wire.NewObject()
		id := int64(100 + index)
		message.Set("id", wire.IntValue(id))
		message.Set("label", wire.TextValue(names[generator.IntN(len(names))]))
		message.Set("body", wire.TextValue(fmt.Sprintf("Message %d", index)))
		message.Set("kind", wire.TextValue(pick(generator, []string{"note", "task", "handoff", "reassign", "question", "blocker", "done"})))
		if generator.IntN(2) == 0 {
			message.Set("to", wire.TextValue(pick(generator, append([]string{me}, names...))))
		}
		kind, _ := message.Get("kind")
		// A task blocker always replies to its task.
		if kind.Text == "blocker" || generator.IntN(2) == 0 {
			message.Set("reply_to", wire.IntValue(1+generator.Int64N(maxTaskID)))
		}
		if generator.IntN(3) == 0 {
			message.Set("due_minutes", wire.IntValue(30))
		}
		if generator.IntN(3) == 0 {
			blockers = append(blockers, wire.IntValue(id))
		}
		messages = append(messages, message)
	}
	page := wire.NewObject()
	page.Set("messages", wire.ListValue(messages))
	page.Set("more", wire.BoolValue(false))
	page.Set("cursor", wire.IntValue(5))
	page.Set("wake", wire.BoolValue(true))
	if generator.IntN(3) == 0 {
		page.Set("brief", wire.TextValue("Brief"))
		page.Set("role", wire.TextValue("lead"))
	}
	if generator.IntN(2) == 0 {
		page.Set("_task_blockers", wire.ListValue(blockers))
	}
	return page
}

// parseLiteral parses a fixed JSON text.
func parseLiteral(text string) wire.Value {
	value, err := wire.Parse([]byte(text))
	if err != nil {
		panic(err)
	}
	return value
}
