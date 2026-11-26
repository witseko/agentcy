package shim

import (
	"crypto/sha256"
	"encoding/hex"
	"sync"

	"github.com/witseko/agentcy/internal/progress"
	"github.com/witseko/agentcy/internal/wire"
)

// Exchanger sends one member request to the core and returns its result.
type Exchanger interface {
	Exchange(request wire.Request) (wire.Value, error)
}

// listenState is the small in-memory state of one shim process that drives guidance:
// the consecutive quiet waits of each listener and the last team snapshot each lead saw.
// Both are keyed by the SHA-256 digest of the member token, so no token is held here.
// Port of silent_waits and last_teams in src/agentcy/mcp/server.py.
type listenState struct {
	mutex       sync.Mutex
	silentWaits map[string]int
	// lastTeams is nil for the fresh worker, which never omits the team snapshot.
	lastTeams map[string]string
}

// newListenState returns an empty state; withTeams enables the team snapshot memory.
func newListenState(withTeams bool) *listenState {
	state := &listenState{silentWaits: map[string]int{}}
	if withTeams {
		state.lastTeams = map[string]string{}
	}
	return state
}

// resetWaits forgets the quiet waits of one listener.
func (state *listenState) resetWaits(digest string) {
	state.mutex.Lock()
	defer state.mutex.Unlock()
	delete(state.silentWaits, digest)
}

// clearWaits forgets the quiet waits of every listener.
func (state *listenState) clearWaits() {
	state.mutex.Lock()
	defer state.mutex.Unlock()
	clear(state.silentWaits)
}

// countWait adds one quiet wait for a listener and returns the new count.
func (state *listenState) countWait(digest string) int {
	state.mutex.Lock()
	defer state.mutex.Unlock()
	state.silentWaits[digest]++
	return state.silentWaits[digest]
}

// teamUnchanged reports whether a lead already saw this team fingerprint, and records it
// otherwise. A fresh worker state never reports a match and records nothing.
func (state *listenState) teamUnchanged(digest, fingerprint string, hasMessages bool) bool {
	state.mutex.Lock()
	defer state.mutex.Unlock()
	if state.lastTeams == nil {
		return false
	}
	if !hasMessages {
		if previous, found := state.lastTeams[digest]; found && previous == fingerprint {
			return true
		}
	}
	state.lastTeams[digest] = fingerprint
	return false
}

// tokenDigest returns the SHA-256 hex digest of a member token.
func tokenDigest(token string) string {
	digest := sha256.Sum256([]byte(token))
	return hex.EncodeToString(digest[:])
}

// readMember reads once from the core and, for a waiting read, uses the latest room state
// for guidance. A waiting read costs two or three exchanges, as in the Python shim.
// Port of read_member in src/agentcy/mcp/server.py.
func readMember(core Exchanger, state *listenState, workspace, token string, after, waitSeconds int64) (wire.Value, error) {
	request := &wire.Read{Workspace: workspace, Token: token, After: after, WaitSeconds: waitSeconds}
	page, err := exchangeValid(core, request)
	if err != nil {
		return wire.Value{}, err
	}
	digest := tokenDigest(token)
	if waitSeconds == 0 {
		page.Delete("_task_blockers")
		if messagesOf(page).Truthy() {
			state.resetWaits(digest)
		}
		return page, nil
	}
	statusValue, err := exchangeValid(core, &wire.Status{Workspace: workspace, Token: token})
	if err != nil {
		return wire.Value{}, err
	}
	status := progress.StatusFromValue(statusValue)
	if needsFreshPage(page, status) {
		page, err = exchangeValid(core, &wire.Read{Workspace: workspace, Token: token, After: after})
		if err != nil {
			return wire.Value{}, err
		}
	}
	unreadOther := 0
	if !flag(page, "wake") && !flag(page, "closed") && !flag(page, "retired") {
		unreadOther = messagesOf(page).Len()
		page.Set("messages", wire.ListValue(nil))
		page.Set("cursor", wire.IntValue(after))
		page.Set("more", wire.BoolValue(false))
	}
	return finishWait(page, status, statusValue, state, digest, unreadOther), nil
}

// needsFreshPage reports a status change that the waiting page missed: the room closed,
// the caller was retired, or the sealed round was released.
func needsFreshPage(page wire.Value, status progress.Status) bool {
	pageRound, _ := page.Get("round")
	roundReleased := pageRound.StringOf() == roundSealed && status.Round == roundCritique
	me, _ := status.Member(status.Participant)
	newlyRetired := progress.IsRetired(me) && !flag(page, "retired")
	return (!status.Open && !flag(page, "closed")) || newlyRetired || roundReleased
}

// flag reads a member of a result with Python truthiness, false when absent.
func flag(object wire.Value, key string) bool {
	value, _ := object.Get(key)
	return value.Truthy()
}

// messagesOf returns the messages list of a page.
func messagesOf(page wire.Value) wire.Value {
	messages, _ := page.Get("messages")
	return messages
}

// finishWait updates a listener's quiet count and attaches guidance to a message page.
// Port of finish_wait in src/agentcy/mcp/server.py.
func finishWait(page wire.Value, status progress.Status, statusValue wire.Value, state *listenState, digest string, unreadOther int) wire.Value {
	me := status.Participant
	caller, _ := status.Member(me)
	retired := progress.IsRetired(caller)
	messages := messagesOf(page).Items
	hasMessages := len(messages) > 0
	count := updateSilentWait(page, status, me, retired, state, digest)
	for _, field := range contextFields {
		page.Delete(field)
	}
	if status.Open && !retired {
		if caller.Role != progress.RoleWorker {
			attachTeamFields(page, status, statusValue, me, hasMessages, state, digest)
		} else {
			attachWorkerFields(page, status, me, unreadOther)
		}
	}
	taskBlockers, _ := page.Delete("_task_blockers")
	blockerReceived := false
	for _, message := range messages {
		if progress.IsCurrentBlocker(messageOf(message), status.Members, containsNumber(taskBlockers, message)) {
			blockerReceived = true
			break
		}
	}
	actionHint := ""
	if hasMessages {
		actionHint = incomingActionHint(messages, me)
	}
	page.Set("next", wire.TextValue(guidance(waitContext{
		status:          status,
		me:              me,
		hasMessages:     hasMessages,
		silentWaits:     count,
		blockerReceived: blockerReceived,
		actionHint:      actionHint,
	})))
	return page
}

// updateSilentWait resets or increments the caller's quiet-wait count and attaches
// waiting_on to a quiet page outside the sealed round.
func updateSilentWait(page wire.Value, status progress.Status, me string, retired bool, state *listenState, digest string) int {
	if messagesOf(page).Truthy() || !status.Open || retired {
		state.resetWaits(digest)
		return 0
	}
	count := state.countWait(digest)
	if status.Round != roundSealed {
		if pending := progress.LeadWaitingOn(status, me); len(pending) > 0 {
			page.Set("waiting_on", wire.TextList(pending))
		}
	}
	return count
}

// messageOf reads the fields of a message that blocker detection needs.
func messageOf(message wire.Value) progress.Message {
	id, _ := message.Get("id")
	label, _ := message.Get("label")
	kind, _ := message.Get("kind")
	replyTo, _ := message.Get("reply_to")
	number, _ := id.IntOf()
	reply, _ := replyTo.IntOf()
	return progress.Message{ID: number, Label: label.StringOf(), Kind: kind.StringOf(), ReplyTo: reply}
}

// containsNumber reports whether a list of message IDs holds the ID of message.
func containsNumber(list wire.Value, message wire.Value) bool {
	id, _ := message.Get("id")
	for _, item := range list.Items {
		if item.Kind == wire.Number && id.Kind == wire.Number && item.Literal == id.Literal {
			return true
		}
	}
	return false
}

// attachTeamFields attaches the lead's team snapshot, or team_unchanged on a quiet wait
// that saw the same team, and an imbalanced token balance without its prose.
func attachTeamFields(page wire.Value, status progress.Status, statusValue wire.Value, me string, hasMessages bool, state *listenState, digest string) {
	snapshot := teamSnapshot(statusValue, me)
	if state.teamUnchanged(digest, teamFingerprint(snapshot), hasMessages) {
		page.Set("team_unchanged", wire.BoolValue(true))
	} else {
		page.Set("team", snapshot)
	}
	balance := status.TokenBalance
	if balance.Truthy() && !flag(balance, "balanced") {
		compact := balance.Copy()
		compact.Delete("recommendation")
		page.Set("token_balance", compact)
	}
}

// teamSnapshot summarizes non-human teammates so a lead sees joins, open tasks, idle
// time, and budgets.
func teamSnapshot(statusValue wire.Value, me string) wire.Value {
	snapshot := wire.NewObject()
	members, _ := statusValue.Get("members")
	for _, name := range members.Keys() {
		member, _ := members.Get(name)
		role, _ := member.Get("role")
		if name == me || role.StringOf() == progress.RoleHuman {
			continue
		}
		state, _ := member.Get("state")
		entry := wire.NewObject()
		entry.Set("state", state)
		for _, field := range []string{"open_tasks", "launch_state", "idle_seconds"} {
			if value, found := member.Get(field); found {
				entry.Set(field, value)
			}
		}
		if usage, found := member.Get("usage"); found {
			compact := wire.NewObject()
			for _, field := range compactUsageFields {
				if value, present := usage.Get(field); present {
					compact.Set(field, value)
				}
			}
			entry.Set("usage", compact)
		}
		snapshot.Set(name, entry)
	}
	return snapshot
}

// teamFingerprint encodes a snapshot without the volatile idle_seconds, so unchanged
// quiet waits can omit the team.
func teamFingerprint(snapshot wire.Value) string {
	fingerprint := snapshot.Copy()
	for _, name := range fingerprint.Keys() {
		entry, _ := fingerprint.Get(name)
		entry.Delete("idle_seconds")
	}
	return fingerprint.Compact()
}

// attachWorkerFields surfaces the count of messages for others and active peer blockers
// on a worker's waiting read.
func attachWorkerFields(page wire.Value, status progress.Status, me string, unreadOther int) {
	if unreadOther > 0 {
		page.Set("unread_other_messages", wire.IntValue(int64(unreadOther)))
	}
	blocked := progress.BlockedPeers(status.Members, me)
	if len(blocked) == 0 {
		return
	}
	entries := make([]wire.Value, 0, len(blocked))
	for _, peer := range blocked {
		entry := wire.NewObject()
		entry.Set("name", wire.TextValue(peer.Name))
		if len(peer.Tasks) > 0 {
			ids := make([]wire.Value, len(peer.Tasks))
			for index, id := range peer.Tasks {
				ids[index] = wire.IntValue(id)
			}
			entry.Set("tasks", wire.ListValue(ids))
		}
		entries = append(entries, entry)
	}
	page.Set("peer_blockers", wire.ListValue(entries))
}

// exchangeValid validates a request built by the shim, then sends it. A validation
// failure has the text pydantic gives when the Python shim builds the model.
func exchangeValid(core Exchanger, request wire.Request) (wire.Value, error) {
	if err := request.Validate(); err != nil {
		return wire.Value{}, err
	}
	return core.Exchange(request)
}
