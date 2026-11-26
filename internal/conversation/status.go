package conversation

import (
	"context"
	"database/sql"

	"github.com/witseko/agentcy/internal/core"
	"github.com/witseko/agentcy/internal/progress"
	"github.com/witseko/agentcy/internal/tokens"
	"github.com/witseko/agentcy/internal/wire"
)

// progressKinds are the kinds that mark a member's latest progress.
var progressKinds = []string{kindDone, kindBlocker, kindResume}

// authorActivity summarizes one author's posts: their count, the latest one, and the
// latest progress post. Lifecycle events are not posts.
type authorActivity struct {
	count    int64
	latestID int64
	marker   string
	markerID int64
}

const selectPostCounts = `SELECT message.author, count(*) AS count_1, max(message.id) AS max_1 
FROM message 
WHERE message.room = ? AND message.kind IS NOT ? GROUP BY message.author`

const selectLatestProgress = `SELECT message.author, message.kind, max(message.id) AS max_1 
FROM message 
WHERE message.room = ? AND message.kind IN (?, ?, ?) GROUP BY message.author, message.kind`

// memberActivity summarizes each author's posts. Both queries read only the
// message_room_kind index.
func memberActivity(ctx context.Context, tx *sql.Tx, room string) (map[int64]authorActivity, error) {
	activity := map[int64]authorActivity{}
	rows, err := tx.QueryContext(ctx, selectPostCounts, room, eventKind)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var author int64
		var entry authorActivity
		if err := rows.Scan(&author, &entry.count, &entry.latestID); err != nil {
			rows.Close()
			return nil, err
		}
		activity[author] = entry
	}
	if err := closeRows(rows); err != nil {
		return nil, err
	}
	rows, err = tx.QueryContext(ctx, selectLatestProgress, room, progressKinds[0], progressKinds[1], progressKinds[2])
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var author, latestID int64
		var kind string
		if err := rows.Scan(&author, &kind, &latestID); err != nil {
			return nil, err
		}
		entry := activity[author]
		if entry.markerID == 0 || latestID > entry.markerID {
			entry.marker = kind
			entry.markerID = latestID
			activity[author] = entry
		}
	}
	return activity, rows.Err()
}

// closeRows closes rows and returns the iteration error.
func closeRows(rows *sql.Rows) error {
	err := rows.Err()
	if closeErr := rows.Close(); err == nil {
		err = closeErr
	}
	return err
}

// statusMember is one member of a status, built from its slot, activity and tasks.
type statusMember struct {
	id          int64
	label       string
	role        int64
	retired     bool
	joined      bool
	listening   bool
	connected   bool
	seen        sql.NullInt64
	launchState sql.NullString
	activity    authorActivity
	openTasks   []taskSummary
	usage       *tokens.MemberUsage
	now         int64
}

// state resolves the member's state: human, invited, retired, or for a joined member its
// progress (progress_state and member_entry in repository.py).
func (member *statusMember) state() string {
	switch {
	case member.role == roleHuman:
		return "human"
	case member.retired:
		return "retired"
	case member.joined:
		return member.progressState()
	}
	return "invited"
}

// progressState resolves a joined member's state from its tasks and recent posts.
func (member *statusMember) progressState() string {
	for _, summary := range member.openTasks {
		if summary.blocked {
			return "blocked"
		}
	}
	if member.activity.count == 0 {
		return "joined"
	}
	if member.activity.marker == kindBlocker {
		return "blocked"
	}
	if member.activity.marker == kindDone && len(member.openTasks) == 0 && member.activity.latestID == member.activity.markerID {
		return "done"
	}
	return "replied"
}

// value describes the member in Python's field order.
func (member *statusMember) value() wire.Value {
	entry := wire.NewObject()
	entry.Set("role", wire.TextValue(roleLabel(member.role)))
	entry.Set("state", wire.TextValue(member.state()))
	if member.role == roleHuman {
		return entry
	}
	if member.launchState.Valid {
		entry.Set("launch_state", wire.TextValue(member.launchState.String))
	}
	if member.joined && !member.retired && member.seen.Valid {
		entry.Set("idle_seconds", wire.IntValue(max(0, member.now-member.seen.Int64)))
	}
	if member.listening {
		entry.Set("listening", wire.BoolValue(true))
	}
	if member.connected {
		entry.Set("connected", wire.BoolValue(true))
	}
	if (member.joined || member.retired) && member.activity.count > 0 {
		entry.Set("messages", wire.IntValue(member.activity.count))
	}
	if len(member.openTasks) > 0 {
		entry.Set("open_tasks", taskSummaryList(member.openTasks))
	}
	if member.usage != nil {
		entry.Set("usage", member.usage.Value())
	}
	return entry
}

// balanceMember is the member as the token balancer reads it.
func (member *statusMember) balanceMember() tokens.BalanceMember {
	progressMember := progress.Member{Name: member.label, Role: roleLabel(member.role), State: member.state()}
	if member.launchState.Valid && member.role != roleHuman {
		launchState := member.launchState.String
		progressMember.LaunchState = &launchState
		progressMember.HasLaunchState = true
	}
	return tokens.BalanceMember{Member: progressMember, Usage: member.usage}
}

const selectStatusSlots = `SELECT participant.id, participant.label, participant.role, participant.retired, participant.token_hash IS NOT NULL AS joined, participant.seen, assignment.state AS launch_state 
FROM participant LEFT OUTER JOIN assignment ON assignment.slot = participant.id AND assignment.invite_hash = participant.invite_hash 
WHERE participant.room = ? ORDER BY participant.id`

// statusMembers loads every slot of a room in member-ID order with its activity and
// open tasks.
func statusMembers(ctx context.Context, tx *sql.Tx, room string, now int64) ([]*statusMember, error) {
	state, err := roomState(ctx, tx, room)
	if err != nil {
		return nil, err
	}
	tasksByOwner := openTasksOf(state.board, now)
	rows, err := tx.QueryContext(ctx, selectStatusSlots, room)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var members []*statusMember
	for rows.Next() {
		member := &statusMember{now: now}
		if err := rows.Scan(&member.id, &member.label, &member.role, &member.retired, &member.joined, &member.seen, &member.launchState); err != nil {
			return nil, err
		}
		member.activity = state.activity[member.id]
		member.openTasks = tasksByOwner[member.id]
		members = append(members, member)
	}
	return members, rows.Err()
}

// tokenStatus is the team balance and total spend that a status adds.
type tokenStatus struct {
	balance *tokens.Balance
	total   *tokens.ConversationUsage
}

// enrichTokenStatus attaches each member's token usage and returns the team balance and
// the conversation's total spend (enrich_token_status in repository.py).
func enrichTokenStatus(ctx context.Context, tx *sql.Tx, book *usageBook, room string, members []*statusMember, now int64) (tokenStatus, error) {
	slots, err := tokens.RoomUsage(ctx, tx, room)
	if err != nil {
		return tokenStatus{}, err
	}
	settings := book.tokenSettings()
	bySlot := map[int64]tokens.SlotUsage{}
	var models []string
	sinceByModel := map[string]*int64{}
	for _, slot := range slots {
		bySlot[slot.Slot] = slot
		if slot.Model == nil || *slot.Model == "" {
			continue
		}
		models = append(models, *slot.Model)
		if settings == nil {
			continue
		}
		if window := settings.ResetWindowSecondsFor(*slot.Model); window != nil {
			since := now - *window
			sinceByModel[*slot.Model] = &since
		}
	}
	spend, err := book.spend(ctx, models, sinceByModel)
	if err != nil {
		return tokenStatus{}, err
	}
	tracking := false
	balanceMembers := make([]tokens.BalanceMember, 0, len(members))
	for _, member := range members {
		if raw, found := bySlot[member.id]; found && member.role != roleHuman {
			member.usage = tokens.BuildMemberUsage(raw, settings, spend)
			tracking = tracking || member.usage != nil
		}
		balanceMembers = append(balanceMembers, member.balanceMember())
	}
	result := tokenStatus{balance: tokens.EvaluateBalance(balanceMembers, settings)}
	if tracking {
		result.total = tokens.SumConversationUsage(slots)
	}
	return result, nil
}

// statusOutcome is the encoded status payload along with the caller's own member entry
// and evaluated token balance.
type statusOutcome struct {
	value   wire.Value
	own     *statusMember
	balance *tokens.Balance
}

// status reports the activity and message counts of the participant's team. The human
// of an operator request may have no row yet; its ID is then zero.
func status(ctx context.Context, tx *sql.Tx, book *usageBook, member *participant, now int64) (statusOutcome, error) {
	members, err := statusMembers(ctx, tx, member.Room, now)
	if err != nil {
		return statusOutcome{}, err
	}
	tokenState, err := enrichTokenStatus(ctx, tx, book, member.Room, members, now)
	if err != nil {
		return statusOutcome{}, err
	}
	entries := wire.NewObject()
	var presence core.RoomPresence
	if member.Open {
		if workspace := workspaceFromContext(ctx); workspace != "" {
			presence = core.PresenceFromContext(ctx, core.Room{Workspace: workspace, Conversation: member.Room})
		}
	}
	var own *statusMember
	for _, entry := range members {
		if member.Open && entry.joined && !entry.retired {
			entry.listening = presence.Listening(entry.id)
			entry.connected = presence.Connected(entry.id)
		}
		entries.Set(entry.label, entry.value())
		if entry.label == member.Label {
			own = entry
		}
	}
	result := wire.NewObject()
	result.Set("open", wire.BoolValue(member.Open))
	result.Set("members", entries)
	result.Set("participant", wire.TextValue(member.Label))
	roundFields(result, member.Round, member.RoundDeadline)
	if tokenState.balance != nil {
		result.Set("token_balance", tokenState.balance.Value())
	}
	if tokenState.total != nil {
		result.Set("token_usage", tokenState.total.Value())
	}
	if member.OpenInviteHash.Valid {
		result.Set("open_invite", wire.BoolValue(true))
	}
	if member.sealed() && member.Role == roleWorker {
		submitted, err := proposalSubmitted(ctx, tx, member)
		if err != nil {
			return statusOutcome{}, err
		}
		result.Set("proposal_submitted", wire.BoolValue(submitted))
	}
	return statusOutcome{value: result, own: own, balance: tokenState.balance}, nil
}
