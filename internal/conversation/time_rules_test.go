package conversation

import (
	"context"
	"database/sql"
	"testing"

	"github.com/witseko/agentcy/internal/wire"
)

// Ported from the former Python test_invites.py: a join code works until its expiry,
// 3600 seconds after it was issued, and not at that second.
func TestJoinCodeExpiresAfterOneHour(t *testing.T) {
	cases := []struct {
		name    string
		offset  int64
		refusal string
	}{
		{"one second before expiry", inviteLifetimeSeconds - 1, ""},
		{"at expiry", inviteLifetimeSeconds, invalidInvitation},
		{"long after", 10 * inviteLifetimeSeconds, invalidInvitation},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			f := newFixture(t)
			created := f.createRoom(wire.Create{Invitees: []wire.Slot{{Name: "Worker"}}}, startUnix)
			_, refusal := f.joinWith(text(created, "invitations", "Worker"), startUnix+testCase.offset)
			if refusal != testCase.refusal {
				t.Errorf("refusal = %q, want %q", refusal, testCase.refusal)
			}
		})
	}
}

// Ported from the former Python test_repository.py: activity is saved at most once per
// 10 seconds.
func TestActivityIsSavedAtMostEveryTenSeconds(t *testing.T) {
	f := newFixture(t)
	token := text(f.createRoom(wire.Create{Invitees: []wire.Slot{{Name: "Worker"}}}, startUnix), "token")
	steps := []struct {
		offset   int64
		wantSeen int64
	}{
		{0, startUnix},
		{activityUpdateSeconds, startUnix},
		{activityUpdateSeconds + 1, startUnix + activityUpdateSeconds + 1},
	}
	for _, step := range steps {
		f.statusOf(token, startUnix+step.offset)
		if seen := f.member(token).Seen.Int64; seen != step.wantSeen {
			t.Errorf("after a request at +%d s: seen = %d, want %d", step.offset, seen, step.wantSeen)
		}
	}
}

// Ported from the former Python test_message_kinds.py: an identical post within 60
// seconds is a retry even with a new submission ID; after that it is a new message.
func TestIdenticalPostsDeduplicateForSixtySeconds(t *testing.T) {
	cases := []struct {
		name     string
		offset   int64
		wantSame bool
	}{
		{"at the window's edge", contentDedupSeconds, true},
		{"after the window", contentDedupSeconds + 1, false},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			f := newFixture(t)
			token := text(f.createRoom(wire.Create{Invitees: []wire.Slot{{Name: "Worker"}}}, startUnix), "token")
			first, _ := f.post(token, submissionRequest{submissionID: "a", body: "Same text", kind: "note"}, startUnix)
			second, _ := f.post(token, submissionRequest{submissionID: "b", body: "Same text", kind: "note"}, startUnix+testCase.offset)
			same := first.Compact() == second.Compact()
			if same != testCase.wantSame {
				t.Errorf("first %s, second %s: same = %v, want %v", first.Compact(), second.Compact(), same, testCase.wantSame)
			}
		})
	}
}

// Ported from the former Python test_tasks.py: a task is overdue only after its due
// time, and a finished task is never overdue.
func TestTasksBecomeOverdueAfterTheirDueTime(t *testing.T) {
	f := newFixture(t)
	created := f.createRoom(wire.Create{Invitees: []wire.Slot{{Name: "Worker"}}}, startUnix)
	lead := text(created, "token")
	worker := text(f.mustJoin(text(created, "invitations", "Worker"), startUnix), "token")
	to, due := "Worker", int64(5)
	posted, refusal := f.post(lead, submissionRequest{submissionID: "t", body: "Do it", kind: kindTask, to: &to, dueMinutes: &due}, startUnix)
	if refusal != "" {
		t.Fatalf("task refused: %s", refusal)
	}
	taskID, _ := field(posted, "message_id").IntOf()
	openTask := func(now int64) wire.Value {
		tasks := field(f.statusOf(lead, now), "members", "Worker", "open_tasks")
		if tasks.Len() == 0 {
			return wire.Value{}
		}
		return tasks.Items[0]
	}
	if openTask(startUnix + 300).Has("overdue") {
		t.Error("overdue exactly at the due time")
	}
	if !openTask(startUnix + 301).Has("overdue") {
		t.Error("not overdue one second after the due time")
	}
	if dueAt, _ := field(openTask(startUnix), "due").IntOf(); dueAt != startUnix+300 {
		t.Errorf("due = %d, want %d", dueAt, startUnix+300)
	}
	if _, refusal := f.post(worker, submissionRequest{submissionID: "d", body: "Finished", kind: kindDone, replyTo: &taskID}, startUnix+400); refusal != "" {
		t.Fatalf("done refused: %s", refusal)
	}
	if openTask(startUnix+500).Kind != wire.Null {
		t.Error("a finished task is still open")
	}
}

// Ported from the former Python test_sealed_rounds.py: a sealed round is released by
// the first read, post or status at or after its deadline, and not before.
func TestSealedRoundReleasesAtItsDeadline(t *testing.T) {
	f := newFixture(t)
	minutes := int64(10)
	created := f.createRoom(wire.Create{Invitees: []wire.Slot{{Name: "A"}, {Name: "B"}}, Sealed: true, DeadlineMinutes: &minutes}, startUnix)
	lead := text(created, "token")
	worker := text(f.mustJoin(text(created, "invitations", "A"), startUnix), "token")
	f.mustJoin(text(created, "invitations", "B"), startUnix)
	if _, refusal := f.post(worker, submissionRequest{submissionID: "p", body: "My idea", kind: kindProposal}, startUnix+1); refusal != "" {
		t.Fatalf("proposal refused: %s", refusal)
	}
	deadline := startUnix + minutes*60
	if round := text(f.statusOf(lead, deadline-1), "round"); round != roundSealed {
		t.Errorf("round before the deadline = %q, want sealed", round)
	}
	released := f.statusOf(lead, deadline)
	if round := text(released, "round"); round != roundCritique {
		t.Errorf("round at the deadline = %q, want critique", round)
	}
	if released.Has("round_deadline") {
		t.Error("a released round still has a deadline")
	}
	page := f.mustWrite(func(ctx context.Context, tx *sql.Tx) (wire.Value, error) {
		member, err := authenticate(ctx, tx, lead)
		if err != nil {
			return wire.Value{}, err
		}
		result, _, err := readPage(ctx, tx, member, 0)
		return result.Page, err
	})
	messages := field(page, "messages").Items
	last := messages[len(messages)-1]
	if body := text(last, "body"); body != "Proposals released for critique. Missing proposals from: B." {
		t.Errorf("release event = %q", body)
	}
}

// mustJoin redeems a code and fails the test on a refusal.
func (f *fixture) mustJoin(code string, now int64) wire.Value {
	f.t.Helper()
	result, refusal := f.joinWith(code, now)
	if refusal != "" {
		f.t.Fatalf("join refused: %s", refusal)
	}
	return result
}
