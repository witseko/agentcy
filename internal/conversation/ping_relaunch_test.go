package conversation

import (
	"context"
	"database/sql"
	"io"
	"log"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/witseko/agentcy/internal/fresh"
	"github.com/witseko/agentcy/internal/paths"
	"github.com/witseko/agentcy/internal/wire"
)

type stubFreshRuns struct {
	ready      bool
	dispatched []fresh.Dispatch
}

func (stub *stubFreshRuns) Ready() bool { return stub.ready }
func (stub *stubFreshRuns) Dispatch(job fresh.Dispatch) {
	stub.dispatched = append(stub.dispatched, job)
}
func (stub *stubFreshRuns) Wake(string) {}

func TestResolvePingEnforcesAuthorityAndTargetRules(t *testing.T) {
	dataDir := t.TempDir()
	checkout, err := paths.RealPath(t.TempDir(), true)
	if err != nil {
		t.Fatal(err)
	}
	workspace, err := paths.WorkspaceKey(checkout, os.Environ())
	if err != nil {
		t.Fatal(err)
	}
	shared := newTestShared(t, dataDir, log.New(io.Discard, "", 0))
	handler := NewHandler(shared, os.Environ(), &stubFreshRuns{ready: true})
	ctx := context.Background()
	now := time.Unix(startUnix, 0)

	// Without a store, both member and operator pings fail closed without creating a file.
	if _, err := handler.ResolveMemberPing(ctx, &wire.Ping{Workspace: workspace, Token: "missing", Name: "Worker"}, now); err == nil {
		t.Fatal("ping on missing store succeeded")
	}
	if _, err := handler.ResolveOperatorPing(ctx, &wire.OperatorPing{
		ConversationHeader: wire.ConversationHeader{
			CheckoutHeader: wire.CheckoutHeader{Directory: checkout},
			Conversation:   "0123456789abcdef01234567",
		},
		Name: "Worker",
	}, now); err == nil {
		t.Fatal("operator_ping on missing store succeeded")
	}
	if shared.Stores.Exists(workspace) {
		t.Fatal("failed pings created a workspace store")
	}

	created, err := handler.Member(ctx, &wire.Create{
		Workspace: workspace, Brief: "Ping checks", Initiator: "Lead",
		Invitees: []wire.Slot{{Name: "Worker"}, {Name: "Retired"}},
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	roomID := text(created.Result, "conversation")
	leadToken := text(created.Result, "token")
	workerJoin, err := handler.Member(ctx, &wire.Join{
		Workspace: workspace, Code: text(created.Result, "invitations", "Worker"),
	}, now.Add(10*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	workerToken := text(workerJoin.Result, "token")
	if _, err := handler.Member(ctx, &wire.Status{Workspace: workspace, Token: workerToken}, now.Add(10*time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := handler.Member(ctx, &wire.Retire{Workspace: workspace, Token: leadToken, Name: "Retired"}, now.Add(20*time.Second)); err != nil {
		t.Fatal(err)
	}

	// OpenSession succeeds for active Worker and rejects retired/unknown tokens.
	sessionStart, err := handler.OpenSession(ctx, &wire.Session{Workspace: workspace, Token: workerToken}, now)
	if err != nil || sessionStart.Room.Conversation != roomID || sessionStart.MemberID == 0 {
		t.Fatalf("OpenSession got %+v, err %v", sessionStart, err)
	}

	// Worker cannot ping; Lead cannot ping self, retired, or unknown.
	rejections := []struct {
		token string
		name  string
		want  string
	}{
		{workerToken, "Lead", "Only a lead or co-lead can ping participants"},
		{leadToken, "Lead", "You cannot ping yourself"},
		{leadToken, "Retired", "Participant is retired: Retired"},
		{leadToken, "Nobody", unknownParticipant},
	}
	for _, check := range rejections {
		_, err := handler.ResolveMemberPing(ctx, &wire.Ping{
			Workspace: workspace, Token: check.token, Name: check.name,
		}, now.Add(30*time.Second))
		if err == nil || err.Error() != check.want {
			t.Errorf("ping(%q): got %v, want %q", check.name, err, check.want)
		}
	}

	// Valid lead ping and operator ping resolve Worker with idle_seconds.
	target, err := handler.ResolveMemberPing(ctx, &wire.Ping{
		Workspace: workspace, Token: leadToken, Name: "worker",
	}, now.Add(40*time.Second))
	if err != nil || target.Label != "Worker" || target.State != "joined" || target.IdleSeconds == nil || *target.IdleSeconds != 30 {
		t.Fatalf("lead ResolveMemberPing got %+v, err %v", target, err)
	}

	// Add Human via an operator post, then verify operator cannot ping Human.
	if _, err := handler.Operator(ctx, &wire.OperatorPost{
		ConversationHeader: wire.ConversationHeader{
			CheckoutHeader: wire.CheckoutHeader{Directory: checkout},
			Conversation:   roomID,
		},
		SubmissionID: "s1", Body: "hi", Kind: "note",
	}, now.Add(40*time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := handler.ResolveOperatorPing(ctx, &wire.OperatorPing{
		ConversationHeader: wire.ConversationHeader{
			CheckoutHeader: wire.CheckoutHeader{Directory: checkout},
			Conversation:   roomID,
		},
		Name: "Human",
	}, now.Add(40*time.Second)); err == nil || err.Error() != "Cannot ping Human" {
		t.Errorf("operator ping Human: %v", err)
	}
}

func TestRelaunchRevivesEndedPluginSlotAndKeepsOpenTasks(t *testing.T) {
	dataDir := t.TempDir()
	checkout, err := paths.RealPath(t.TempDir(), true)
	if err != nil {
		t.Fatal(err)
	}
	workspace, err := paths.WorkspaceKey(checkout, os.Environ())
	if err != nil {
		t.Fatal(err)
	}
	pluginsJSON := `{"plugins":{"glm":{"command":["/bin/echo"],"model":"zai/glm-5"},"alt":{"command":["/bin/echo"],"model":"google/gemini-pro"}}}`
	if err := os.WriteFile(filepath.Join(dataDir, "plugins.json"), []byte(pluginsJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	shared := newTestShared(t, dataDir, log.New(io.Discard, "", 0))
	runs := &stubFreshRuns{ready: true}
	handler := NewHandler(shared, os.Environ(), runs)
	ctx := context.Background()
	now := time.Unix(startUnix, 0)

	created, err := handler.Member(ctx, &wire.Create{
		Workspace: workspace, Brief: "Relaunch test", Initiator: "Lead",
		Invitees: []wire.Slot{{Name: "Worker"}},
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	roomID := text(created.Result, "conversation")
	leadToken := text(created.Result, "token")

	// Initial plugin launch of Worker.
	firstLaunch, err := handler.Member(ctx, &wire.Launch{
		Workspace: workspace, Token: leadToken, Name: "Worker", Plugin: "glm", Directory: checkout,
	}, now)
	if err != nil || len(runs.dispatched) != 1 {
		t.Fatalf("initial launch err=%v, dispatched=%d", err, len(runs.dispatched))
	}
	firstCode := runs.dispatched[0].Start.JoinCode
	firstAssignID := text(firstLaunch.Result, "assignment_id")

	// Relaunch while still queued/active is refused.
	if _, err := handler.Member(ctx, &wire.Launch{
		Workspace: workspace, Token: leadToken, Name: "Worker", Plugin: "glm", Directory: checkout, Relaunch: true,
	}, now); err == nil || err.Error() != "Fresh run is still active: Worker" {
		t.Fatalf("active relaunch got %v", err)
	}

	// Worker joins using firstCode, Lead assigns task #3, then the fresh run fails.
	joined, err := handler.Member(ctx, &wire.Join{Workspace: workspace, Code: firstCode}, now)
	if err != nil {
		t.Fatal(err)
	}
	oldWorkerToken := text(joined.Result, "token")
	toWorker := "Worker"
	due := int64(15)
	if _, err := handler.Member(ctx, &wire.Post{
		Workspace: workspace, Token: leadToken, SubmissionID: "task-1",
		Body: "Check retry loop", Kind: "task", To: &toWorker, DueMinutes: &due,
	}, now); err != nil {
		t.Fatal(err)
	}
	workspaceStore, err := shared.Stores.Open(ctx, workspace)
	if err != nil {
		t.Fatal(err)
	}
	if err := workspaceStore.Write(ctx, func(tx *sql.Tx) error {
		_, execErr := tx.ExecContext(ctx, "UPDATE assignment SET state=? WHERE id=?", fresh.StateFailed, firstAssignID)
		return execErr
	}); err != nil {
		t.Fatal(err)
	}

	// Operator launch without relaunch is refused with the relaunch hint.
	if _, err := handler.Operator(ctx, &wire.OperatorLaunch{
		ConversationHeader: wire.ConversationHeader{
			CheckoutHeader: wire.CheckoutHeader{Directory: checkout},
			Conversation:   roomID,
		},
		Name: "Worker", Plugin: "alt",
	}, now); err == nil || err.Error() != launchEndedError {
		t.Fatalf("operator launch without relaunch got %v", err)
	}

	// Operator relaunch with a second plugin revokes oldWorkerToken, keeps open tasks, and dispatches a new run.
	relaunched, err := handler.Operator(ctx, &wire.OperatorLaunch{
		ConversationHeader: wire.ConversationHeader{
			CheckoutHeader: wire.CheckoutHeader{Directory: checkout},
			Conversation:   roomID,
		},
		Name: "worker", Plugin: "alt", Relaunch: true,
	}, now.Add(time.Minute))
	if err != nil || len(runs.dispatched) != 2 {
		t.Fatalf("operator relaunch err=%v, dispatched=%d", err, len(runs.dispatched))
	}
	secondAssignID := text(relaunched.Result, "assignment_id")
	if secondAssignID == firstAssignID {
		t.Error("relaunch reused the old assignment_id")
	}
	if _, err := handler.Member(ctx, &wire.Status{Workspace: workspace, Token: oldWorkerToken}, now.Add(time.Minute)); err == nil {
		t.Error("old worker token remained valid after relaunch")
	}

	secondCode := runs.dispatched[1].Start.JoinCode
	rejoined, err := handler.Member(ctx, &wire.Join{Workspace: workspace, Code: secondCode}, now.Add(time.Minute))
	if err != nil {
		t.Fatalf("rejoined worker failed: %v", err)
	}
	statusRes, err := handler.Member(ctx, &wire.Status{Workspace: workspace, Token: text(rejoined.Result, "token")}, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	tasks := field(statusRes.Result, "members", "Worker", "open_tasks")
	if tasks.Len() != 1 {
		t.Fatalf("expected 1 preserved open task on relaunched Worker, got %s", statusRes.Result.Compact())
	}
	if len(relaunched.RevokeSessions) != 1 || relaunched.RevokeSessions[0].MemberID == 0 {
		t.Errorf("expected relaunch to revoke old session stream, got %+v", relaunched.RevokeSessions)
	}
}

func TestRelaunchRefusesUnknownStateAndLiveManualChat(t *testing.T) {
	dataDir := t.TempDir()
	checkout, err := paths.RealPath(t.TempDir(), true)
	if err != nil {
		t.Fatal(err)
	}
	workspace, err := paths.WorkspaceKey(checkout, os.Environ())
	if err != nil {
		t.Fatal(err)
	}
	pluginsJSON := `{"plugins":{"glm":{"command":["/bin/echo"],"model":"zai/glm-5"}}}`
	if err := os.WriteFile(filepath.Join(dataDir, "plugins.json"), []byte(pluginsJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	shared := newTestShared(t, dataDir, log.New(io.Discard, "", 0))
	runs := &stubFreshRuns{ready: true}
	handler := NewHandler(shared, os.Environ(), runs)
	ctx := context.Background()
	now := time.Unix(startUnix, 0)

	created, err := handler.Member(ctx, &wire.Create{
		Workspace: workspace, Brief: "Guard test", Initiator: "Lead",
		Invitees: []wire.Slot{{Name: "Manual"}, {Name: "Fresh"}},
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	leadToken := text(created.Result, "token")

	// Manual joins as an interactive chat without any fresh-run assignment.
	if _, err := handler.Member(ctx, &wire.Join{
		Workspace: workspace, Code: text(created.Result, "invitations", "Manual"),
	}, now); err != nil {
		t.Fatal(err)
	}
	if _, err := handler.Member(ctx, &wire.Launch{
		Workspace: workspace, Token: leadToken, Name: "Manual", Plugin: "glm", Directory: checkout, Relaunch: true,
	}, now); err == nil || err.Error() != "Participant Manual has no ended fresh run; use reinvite to replace a live chat" {
		t.Fatalf("relaunch of live manual chat got %v", err)
	}

	// Fresh launches, then enters uncertain unknown state; relaunch must refuse.
	launched, err := handler.Member(ctx, &wire.Launch{
		Workspace: workspace, Token: leadToken, Name: "Fresh", Plugin: "glm", Directory: checkout,
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	workspaceStore, err := shared.Stores.Open(ctx, workspace)
	if err != nil {
		t.Fatal(err)
	}
	if err := workspaceStore.Write(ctx, func(tx *sql.Tx) error {
		_, execErr := tx.ExecContext(ctx, "UPDATE assignment SET state=? WHERE id=?", fresh.StateUnknown, text(launched.Result, "assignment_id"))
		return execErr
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := handler.Member(ctx, &wire.Launch{
		Workspace: workspace, Token: leadToken, Name: "Fresh", Plugin: "glm", Directory: checkout, Relaunch: true,
	}, now); err == nil || err.Error() != "Fresh run state is unknown for Fresh. Check the provider before launching a replacement" {
		t.Fatalf("relaunch of unknown state got %v", err)
	}

	// Reinvite, retire, and close all populate RevokeSessions.
	reinvited, err := handler.Member(ctx, &wire.Invite{
		Workspace: workspace, Token: leadToken, Name: "Manual", Reinvite: true,
	}, now)
	if err != nil || len(reinvited.RevokeSessions) != 1 || reinvited.RevokeSessions[0].MemberID == 0 {
		t.Fatalf("reinvite revoke sessions = %+v, err = %v", reinvited.RevokeSessions, err)
	}
	retired, err := handler.Member(ctx, &wire.Retire{
		Workspace: workspace, Token: leadToken, Name: "Manual",
	}, now)
	if err != nil || len(retired.RevokeSessions) != 1 || retired.RevokeSessions[0].MemberID == 0 {
		t.Fatalf("retire revoke sessions = %+v, err = %v", retired.RevokeSessions, err)
	}
	closed, err := handler.Member(ctx, &wire.Close{
		Workspace: workspace, Token: leadToken,
	}, now)
	if err != nil || len(closed.RevokeSessions) != 1 || closed.RevokeSessions[0].MemberID != 0 {
		t.Fatalf("close revoke sessions = %+v, err = %v", closed.RevokeSessions, err)
	}
}
