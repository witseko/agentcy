package shim

import (
	"strings"
	"testing"

	"github.com/witseko/agentcy/internal/progress"
	"github.com/witseko/agentcy/internal/wire"
)

// parse decodes a JSON text for a test.
func parse(t *testing.T, text string) wire.Value {
	t.Helper()
	value, err := wire.Parse([]byte(text))
	if err != nil {
		t.Fatalf("parse %s: %v", text, err)
	}
	return value
}

// finish runs finishWait for a page and status after priorWaits quiet waits.
func finish(t *testing.T, state *listenState, page, status string, priorWaits, unreadOther int) wire.Value {
	t.Helper()
	statusValue := parse(t, status)
	if priorWaits > 0 {
		state.silentWaits["digest"] = priorWaits
	}
	return finishWait(parse(t, page), progress.StatusFromValue(statusValue), statusValue, state, "digest", unreadOther)
}

// nextOf returns the guidance of a finished page.
func nextOf(page wire.Value) string {
	next, _ := page.Get("next")
	return next.StringOf()
}

// guide runs guidance for a status without a participant field, like server.guidance.
func guide(t *testing.T, status, me string, hasMessages bool, waits int) string {
	t.Helper()
	return guidance(waitContext{status: progress.StatusFromValue(parse(t, status)), me: me, hasMessages: hasMessages, silentWaits: waits})
}

func TestGuidanceFollowsMemberProgress(t *testing.T) {
	cases := []struct {
		caller, peerState string
		hasMessages       bool
		waits             int
		expected          string
	}{
		{"lead", "invited", true, 8, "Handle these messages"},
		{"lead", "done", false, 1, "All other members are done. Stop listening"},
		{"lead", "replied", false, 1, "Still working: Peer. To keep listening"},
		{"lead", "blocked", false, 1, "tell your user who is blocked: Peer"},
		{"lead", "joined", false, 79, "Still working: Peer. To keep listening"},
		{"co-lead", "joined", false, 80, "Stop listening and tell your user"},
		{"lead", "invited", false, 1, "join codes are unused: Peer"},
		{"worker", "joined", false, 3, "You have not posted your part"},
		{"worker", "replied", false, 3, "No new messages. To keep listening"},
		{"worker", "replied", false, 79, "No new messages. To keep listening"},
		{"worker", "replied", false, 80, "Stop listening and tell your user"},
		{"worker", "done", false, 3, "Stop listening and tell your user"},
	}
	for _, test := range cases {
		status := `{"open": true, "members": {"Me": {"role": "` + test.caller + `", "state": "` + test.peerState +
			`"}, "Peer": {"role": "worker", "state": "` + test.peerState + `"}}}`
		if got := guide(t, status, "Me", test.hasMessages, test.waits); !strings.Contains(got, test.expected) {
			t.Errorf("%+v: next = %q", test, got)
		}
	}
}

func TestLeadReportsEndedFreshWorkerAndTracksAvailablePeer(t *testing.T) {
	for _, launchState := range []string{"failed", "cancelled", "timed_out", "finished", "unknown"} {
		status := `{"open": true, "participant": "Lead", "members": {"Lead": {"role": "lead", "state": "replied"},
			"Fresh": {"role": "worker", "state": "replied", "launch_state": "` + launchState + `", "open_tasks": [{"id": 17}]},
			"Peer": {"role": "worker", "state": "joined"},
			"Done": {"role": "worker", "state": "done", "launch_state": "finished"}}}`
		mixed := finish(t, newListenState(true), `{"messages": []}`, status, 0, 0)
		if waiting, _ := mixed.Get("waiting_on"); waiting.Compact() != `["Peer"]` {
			t.Errorf("%s: waiting_on = %s", launchState, waiting.Compact())
		}
		next := nextOf(mixed)
		for _, want := range []string{"Fresh (fresh run " + launchState + ")", "#17 for Fresh", "keep listening"} {
			if !strings.Contains(next, want) {
				t.Errorf("%s: next %q lacks %q", launchState, next, want)
			}
		}
		if strings.Contains(next, "Done (fresh run") {
			t.Errorf("%s: next names the finished run", launchState)
		}
	}
}

func TestLeadWaitsForStartingFreshWorkerWhenAManualTaskCannotStart(t *testing.T) {
	status := `{"open": true, "participant": "Lead", "members": {"Lead": {"role": "lead", "state": "replied"},
		"Manual": {"role": "worker", "state": "invited", "open_tasks": [{"id": 8}]},
		"Fresh": {"role": "worker", "state": "invited", "launch_state": "starting"}}}`
	page := finish(t, newListenState(true), `{"messages": []}`, status, 0, 0)
	waiting, _ := page.Get("waiting_on")
	if waiting.Compact() != `["Fresh"]` || !strings.Contains(nextOf(page), "#8 for Manual (invitation unused)") {
		t.Errorf("waiting_on %s, next %q", waiting.Compact(), nextOf(page))
	}
}

func TestRetiredMemberGetsStopGuidance(t *testing.T) {
	status := `{"open": true, "participant": "Worker", "members": {"Lead": {"role": "lead", "state": "replied"},
		"Worker": {"role": "worker", "state": "retired"}}}`
	page := finish(t, newListenState(true), `{"messages": [], "retired": true}`, status, 2, 0)
	if nextOf(page) != nextAfterRetire || page.Has("team") {
		t.Errorf("page %s", page.Compact())
	}
	leadStatus := strings.Replace(status, `"participant": "Worker"`, `"participant": "Lead"`, 1)
	leadPage := finish(t, newListenState(true), `{"messages": []}`, leadStatus, 0, 0)
	if leadPage.Has("waiting_on") || !strings.Contains(nextOf(leadPage), "All other members are done") {
		t.Errorf("lead page %s", leadPage.Compact())
	}
}

func TestLeadWaitsForOpenInviteJoiners(t *testing.T) {
	status := `{"open": true, "open_invite": true, "participant": "Lead", "members": {"Lead": {"role": "lead", "state": "replied"}}}`
	first := finish(t, newListenState(true), `{"messages": []}`, status, 0, 0)
	if !strings.Contains(nextOf(first), "Waiting for teammates to join via the open invite") {
		t.Errorf("first next %q", nextOf(first))
	}
	final := finish(t, newListenState(true), `{"messages": []}`, status, leadIdleWaits-1, 0)
	if !strings.Contains(nextOf(final), "No teammates have joined yet via the open invite") {
		t.Errorf("final next %q", nextOf(final))
	}
}

func TestLeadCheckinNamesStaleWorkerAndWorkerSeesPeerBlockers(t *testing.T) {
	leadStatus := `{"open": true, "participant": "Lead", "members": {"Lead": {"role": "lead", "state": "replied"},
		"Slow": {"role": "worker", "state": "replied", "idle_seconds": 150, "open_tasks": [{"id": 7}]}}}`
	page := finish(t, newListenState(true), `{"messages": []}`, leadStatus, leadCheckinWaits-1, 0)
	if want := "Slow has been quiet for 150s on #7 — check in with to='Slow'"; !strings.Contains(nextOf(page), want) {
		t.Errorf("next %q", nextOf(page))
	}
	workerStatus := `{"open": true, "participant": "Helper", "members": {"Lead": {"role": "lead", "state": "done"},
		"Helper": {"role": "worker", "state": "done"},
		"Stuck": {"role": "worker", "state": "blocked", "open_tasks": [{"id": 9, "blocked": true}]}}}`
	done := finish(t, newListenState(true), `{"messages": []}`, workerStatus, 0, 2)
	unread, _ := done.Get("unread_other_messages")
	blockers, _ := done.Get("peer_blockers")
	if unread.Compact() != "2" || blockers.Compact() != `[{"name":"Stuck","tasks":[9]}]` || !strings.Contains(nextOf(done), "Stop listening") {
		t.Errorf("done page %s", done.Compact())
	}
	active := finish(t, newListenState(true), `{"messages": []}`, strings.Replace(workerStatus, `"lead", "state": "done"`, `"lead", "state": "replied"`, 1), 3, 0)
	if !strings.Contains(nextOf(active), "Stuck is blocked") {
		t.Errorf("active next %q", nextOf(active))
	}
}

func TestLeadKeepsListeningWhileTaskOwnerWorks(t *testing.T) {
	status := `{"open": true, "members": {"Lead": {"role": "lead", "state": "replied"},
		"Peer": {"role": "worker", "state": "replied", "open_tasks": [{"id": 4}]}}}`
	if got := guide(t, status, "Lead", false, 79); !strings.Contains(got, "To keep listening") {
		t.Errorf("79 waits: %q", got)
	}
	if got := guide(t, status, "Lead", false, 80); !strings.Contains(got, "Stop listening") {
		t.Errorf("80 waits: %q", got)
	}
}

func TestSilentWaitsOmitUnchangedTeam(t *testing.T) {
	status := `{"open": true, "participant": "Lead", "members": {"Lead": {"role": "lead", "state": "replied"},
		"W": {"role": "worker", "state": "replied", "idle_seconds": 3}}, "token_balance": {"balanced": true}}`
	state := newListenState(true)
	first := finish(t, state, `{"messages": []}`, status, 0, 0)
	second := finish(t, state, `{"messages": []}`, strings.Replace(status, `"idle_seconds": 3`, `"idle_seconds": 9`, 1), 0, 0)
	if !first.Has("team") || !second.Has("team_unchanged") || second.Has("team") || second.Has("token_balance") {
		t.Errorf("first %s\nsecond %s", first.Compact(), second.Compact())
	}
	fresh := newListenState(false)
	finish(t, fresh, `{"messages": []}`, status, 0, 0)
	if again := finish(t, fresh, `{"messages": []}`, status, 0, 0); !again.Has("team") {
		t.Error("the fresh worker omitted the team")
	}
}

func TestIncomingActionHints(t *testing.T) {
	messages := parse(t, `[{"id": 3, "label": "Lead", "kind": "task", "to": "Alpha", "due_minutes": 30},
		{"id": 4, "label": "Alpha", "kind": "handoff", "to": "Bravo", "reply_to": 3},
		{"id": 5, "label": "Lead", "kind": "question", "to": "Alpha"}]`).Items
	if got := incomingActionHint(messages[:1], "Alpha"); !strings.Contains(got, "New task #3 assigned to you, due in 30 minutes") {
		t.Errorf("task hint %q", got)
	}
	if got := incomingActionHint(messages, "Bravo"); got != "Handoff #4 offered to you for task #3. Reply with reply_to=4 using kind='accept' or kind='decline'." {
		t.Errorf("handoff hint %q", got)
	}
	if got := incomingActionHint(messages, "Alpha"); !strings.HasPrefix(got, "Direct question #5 from Lead") {
		t.Errorf("question hint %q", got)
	}
}
