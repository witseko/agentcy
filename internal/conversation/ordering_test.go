package conversation

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/witseko/agentcy/internal/wire"
)

// The roster and the "Active members" list come out in SQLite's byte order of labels,
// because their queries have no ORDER BY and SQLite walks the (room, label) index; status
// lists members in member-ID order. The Python core gives the same orders.
func TestRosterAndRecipientErrorsUseByteOrderAndStatusUsesIDOrder(t *testing.T) {
	f := newFixture(t)
	names := []string{"zeta", "Ärger", "Alpha", "beta", "_under"}
	slots := make([]wire.Slot, 0, len(names))
	for _, name := range names {
		slots = append(slots, wire.Slot{Name: name})
	}
	created := f.createRoom(wire.Create{Initiator: "Mid", Invitees: slots}, startUnix)
	wantRoster := []string{"Alpha", "Mid", "_under", "beta", "zeta", "Ärger"}
	if got := field(created, "roster").Keys(); strings.Join(got, ",") != strings.Join(wantRoster, ",") {
		t.Errorf("roster order = %v, want %v", got, wantRoster)
	}
	lead := text(created, "token")
	ghost := "Ghost"
	_, refusal := f.post(lead, submissionRequest{submissionID: "x", body: "Hi", kind: "note", to: &ghost}, startUnix)
	wantError := "Unknown recipient 'Ghost' in this conversation. Active members: " + strings.Join(wantRoster, ", ")
	if refusal != wantError {
		t.Errorf("refusal = %q, want %q", refusal, wantError)
	}
	wantStatus := append([]string{"Mid"}, names...)
	if got := field(f.statusOf(lead, startUnix), "members").Keys(); strings.Join(got, ",") != strings.Join(wantStatus, ",") {
		t.Errorf("status order = %v, want %v", got, wantStatus)
	}
}

// Python builds each result dict in statement order: join puts cursor before more, a
// read puts more before cursor, and round fields come last.
func TestResultFieldOrderFollowsPython(t *testing.T) {
	f := newFixture(t)
	minutes := int64(20)
	created := f.createRoom(wire.Create{Invitees: []wire.Slot{{Name: "Worker"}}, Sealed: true, DeadlineMinutes: &minutes}, startUnix)
	wantCreate := "conversation,token,invitations,join_prompt,role,roster,round,round_deadline"
	if got := strings.Join(created.Keys(), ","); got != wantCreate {
		t.Errorf("create keys = %s, want %s", got, wantCreate)
	}
	joined := f.mustJoin(text(created, "invitations", "Worker"), startUnix)
	wantJoin := "conversation,participant,role,roster,brief,token,messages,cursor,more,round,round_deadline"
	if got := strings.Join(joined.Keys(), ","); got != wantJoin {
		t.Errorf("join keys = %s, want %s", got, wantJoin)
	}
	worker := f.member(text(joined, "token"))
	page := f.mustWrite(func(ctx context.Context, tx *sql.Tx) (wire.Value, error) {
		result, _, err := readPage(ctx, tx, worker, 0)
		return result.Page, err
	})
	wantRead := "messages,more,cursor,wake,round,round_deadline,brief,role,roster"
	if got := strings.Join(page.Keys(), ","); got != wantRead {
		t.Errorf("read keys = %s, want %s", got, wantRead)
	}
}
