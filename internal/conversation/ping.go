package conversation

import (
	"context"
	"database/sql"
	"time"

	"github.com/witseko/agentcy/internal/core"
	"github.com/witseko/agentcy/internal/wire"
)

// OpenSession authenticates a shim's session stream for an active member of an open
// conversation, without creating a store for an unknown workspace.
func (handler *Handler) OpenSession(ctx context.Context, request *wire.Session, _ time.Time) (core.WaitStart, error) {
	if !handler.stores.Exists(request.Workspace) {
		return core.WaitStart{}, reject(invalidToken)
	}
	var start core.WaitStart
	_, err := handler.readResult(ctx, request.Workspace, func(ctx context.Context, tx *sql.Tx) (wire.Value, error) {
		member, err := authenticate(ctx, tx, request.Token)
		if err != nil {
			return wire.Value{}, err
		}
		if !member.Open {
			return wire.Value{}, reject(conversationClosed)
		}
		if member.Retired {
			return wire.Value{}, reject(retiredError)
		}
		start = core.WaitStart{
			Room:     core.Room{Workspace: request.Workspace, Conversation: member.Room},
			MemberID: member.ID,
		}
		return wire.Value{}, nil
	})
	return start, err
}

// ResolveMemberPing authorizes a lead or co-lead ping and loads the target participant.
func (handler *Handler) ResolveMemberPing(ctx context.Context, request *wire.Ping, now time.Time) (core.PingTarget, error) {
	unix := now.Unix()
	if !handler.stores.Exists(request.Workspace) {
		return core.PingTarget{}, reject(invalidToken)
	}
	var target core.PingTarget
	_, err := handler.writeResult(ctx, request.Workspace, func(ctx context.Context, tx *sql.Tx) (wire.Value, error) {
		actor, err := refreshParticipant(ctx, tx, request.Token, true, unix)
		if err != nil {
			return wire.Value{}, err
		}
		if err := checkManager(actor, "Only a lead or co-lead can ping participants"); err != nil {
			return wire.Value{}, err
		}
		resolved, err := findPingTarget(ctx, tx, request.Workspace, actor.Room, request.Name, actor.ID, true, unix)
		target = resolved
		return wire.Value{}, err
	})
	return target, err
}

// ResolveOperatorPing authorizes a human operator ping and loads the target participant.
func (handler *Handler) ResolveOperatorPing(ctx context.Context, request *wire.OperatorPing, now time.Time) (core.PingTarget, error) {
	unix := now.Unix()
	workspace, err := handler.checkoutWorkspace(request.Directory)
	if err != nil {
		return core.PingTarget{}, err
	}
	if !handler.stores.Exists(workspace) {
		return core.PingTarget{}, reject(unknownConversation)
	}
	var target core.PingTarget
	_, err = handler.readResult(ctx, workspace, func(ctx context.Context, tx *sql.Tx) (wire.Value, error) {
		human, err := humanActor(ctx, tx, request.Conversation)
		if err != nil {
			return wire.Value{}, err
		}
		if !human.Open {
			return wire.Value{}, reject(conversationClosed)
		}
		resolved, err := findPingTarget(ctx, tx, workspace, request.Conversation, request.Name, 0, false, unix)
		target = resolved
		return wire.Value{}, err
	})
	return target, err
}

// findPingTarget resolves name in room and rejects pinging Human, oneself, or a retired
// slot.
func findPingTarget(ctx context.Context, tx *sql.Tx, workspace, room, name string, callerID int64, hasCaller bool, now int64) (core.PingTarget, error) {
	members, err := statusMembers(ctx, tx, room, now)
	if err != nil {
		return core.PingTarget{}, err
	}
	for _, entry := range members {
		if !sameName(entry.label, name) {
			continue
		}
		if entry.role == roleHuman {
			return core.PingTarget{}, rejectf("Cannot ping %s", humanLabel)
		}
		if hasCaller && entry.id == callerID {
			return core.PingTarget{}, reject("You cannot ping yourself")
		}
		if entry.retired {
			return core.PingTarget{}, rejectf("Participant is retired: %s", entry.label)
		}
		target := core.PingTarget{
			Room:     core.Room{Workspace: workspace, Conversation: room},
			MemberID: entry.id,
			Label:    entry.label,
			State:    entry.state(),
		}
		if entry.launchState.Valid {
			launch := entry.launchState.String
			target.LaunchState = &launch
		}
		if entry.joined && entry.seen.Valid {
			idle := max(0, now-entry.seen.Int64)
			target.IdleSeconds = &idle
		}
		return target, nil
	}
	return core.PingTarget{}, reject(unknownParticipant)
}
