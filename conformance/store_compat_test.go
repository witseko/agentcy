//go:build conformance

package conformance

import (
	"os"
	"strings"
	"testing"
	"time"
)

// TestExistingStoreCarriesOver fills a store with the reference daemon, stops it, starts
// the daemon under test on the same AGENTCY_HOME, and checks that status, read, search,
// task and history requests answer the same. Join codes issued by each daemon are
// redeemed by the other.
//
//	AGENTCY_REFERENCE_CORE_CMD="uv run python -I -m agentcy.mcp.core" \
//	AGENTCY_CORE_CMD="$PWD/bin/agentcy core" go test -tags conformance ./conformance/... -run TestExistingStoreCarriesOver
func TestExistingStoreCarriesOver(t *testing.T) {
	reference := strings.Fields(os.Getenv(referenceCommandVariable))
	tested := strings.Fields(os.Getenv(coreCommandVariable))
	if len(reference) == 0 || len(tested) == 0 {
		t.Skipf("set %s and %s to the two daemon commands", referenceCommandVariable, coreCommandVariable)
	}
	start := time.Now().Unix()
	home := shortTempDir(t, "agentcy-home-")
	directory := shortTempDir(t, "agentcy-work-")
	resolved := resolvePath(t, directory)
	side := &walkSide{name: "reference", normalizer: newNormalizer(start)}
	side.normalizer.addLiteral(home, "<home>")
	side.normalizer.addLiteral(resolved, "<checkout>")
	side.normalizer.addLiteral(directory, "<checkout>")
	workspace := sha256Hex(resolved)
	side.daemon = startDaemonInHome(t, reference, home)
	session := &compatSession{t: t, side: side, workspace: workspace, directory: directory, start: start}
	state := session.fill()
	pythonCode := session.member("invite", fields{"token": state.lead, "name": "Late"}).text("code")
	before := session.observe(state)
	side.daemon.stop()

	side.daemon = startDaemonInHome(t, tested, home)
	after := session.observe(state)
	for index := range before {
		if before[index] != after[index] {
			t.Errorf("request %d answers differently after the switch\nreference: %s\ntested:    %s", index+1, before[index], after[index])
		}
	}
	if token := session.member("join", fields{"code": pythonCode}).text("token"); token == "" {
		t.Error("the tested daemon does not redeem a code issued by the reference daemon")
	}
	goCode := session.member("invite", fields{"token": state.lead, "name": "Later"}).text("code")
	side.daemon.stop()

	side.daemon = startDaemonInHome(t, reference, home)
	if token := session.member("join", fields{"code": goCode}).text("token"); token == "" {
		t.Error("the reference daemon does not redeem a code issued by the tested daemon")
	}
}

// compatSession sends requests to whichever daemon currently serves the home.
type compatSession struct {
	t         *testing.T
	side      *walkSide
	workspace string
	directory string
	start     int64
}

// compatState is what fill created.
type compatState struct {
	lead, worker, room, closedRoom, closedLead string
}

// send sends one request and returns its reply and normalised text.
func (session *compatSession) send(request fields) (walkReply, string) {
	session.t.Helper()
	return sendWalkRequest(session.t, session.side, request, session.start)
}

// member sends a member request for the session's workspace.
func (session *compatSession) member(action string, request fields) walkReply {
	session.t.Helper()
	request["action"] = action
	request["workspace"] = session.workspace
	reply, _ := session.send(request)
	if reply.errorText != "" {
		session.t.Fatalf("%s refused: %s", action, reply.errorText)
	}
	return reply
}

// operator sends an operator request with the current daemon's key.
func (session *compatSession) operator(action string, request fields) string {
	session.t.Helper()
	request["version"] = 1
	request["key"] = session.side.daemon.operatorKey(session.t)
	request["action"] = action
	request["directory"] = session.directory
	_, shown := session.send(request)
	return shown
}

// fill creates an open conversation with a task, a handoff and a blocker, and a closed
// one, through the reference daemon.
func (session *compatSession) fill() compatState {
	created := session.member("create", fields{"brief": "Carry the store over\nDetails", "initiator": "Lead",
		"invitees": []any{"Worker", "Helper"}})
	state := compatState{lead: created.text("token"), room: created.text("conversation")}
	state.worker = session.member("join", fields{"code": created.text("invitations", "Worker")}).text("token")
	helper := session.member("join", fields{"code": created.text("invitations", "Helper")}).text("token")
	task := session.member("post", fields{"token": state.lead, "submission_id": "t1", "body": "Port the retry rule",
		"kind": "task", "to": "Worker", "due_minutes": 30}).number("message_id")
	session.member("post", fields{"token": state.worker, "submission_id": "b1", "body": "Blocked on the fixture",
		"kind": "blocker", "reply_to": task})
	session.member("post", fields{"token": state.worker, "submission_id": "h1", "body": "Please take it",
		"kind": "handoff", "to": "Helper", "reply_to": task})
	session.member("post", fields{"token": helper, "submission_id": "n1", "body": "Searching for café words"})
	session.member("report_usage", fields{"token": helper, "input_tokens": 1200, "output_tokens": 300, "model": "model-a"})
	other := session.member("create", fields{"brief": "Finished work", "initiator": "Lead", "invitees": []any{"Worker"}})
	state.closedRoom = other.text("conversation")
	state.closedLead = other.text("token")
	session.member("post", fields{"token": other.text("token"), "submission_id": "c1", "body": "All done here"})
	session.member("close", fields{"token": other.text("token"), "summary": "Closed with a summary"})
	return state
}

// observe sends the read-only requests and returns their normalised replies.
func (session *compatSession) observe(state compatState) []string {
	session.t.Helper()
	var shown []string
	for _, request := range []fields{
		{"action": "status", "workspace": session.workspace, "token": state.lead},
		// A read records usage, so it reads the closed conversation, whose usage no other
		// request here shows.
		{"action": "read", "workspace": session.workspace, "token": state.closedLead},
		{"action": "list_closed", "workspace": session.workspace},
		{"action": "read_closed", "workspace": session.workspace, "conversation": state.closedRoom},
	} {
		_, text := session.send(request)
		shown = append(shown, text)
	}
	shown = append(shown,
		session.operator("operator_list", fields{}),
		session.operator("operator_show", fields{"conversation": state.room}),
		session.operator("operator_tasks", fields{"conversation": state.room}),
		session.operator("operator_search", fields{"query": "café retry"}),
	)
	return shown
}
