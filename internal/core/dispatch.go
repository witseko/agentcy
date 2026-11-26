package core

import (
	"context"

	"github.com/witseko/agentcy/internal/wire"
)

// dispatch routes one validated request. Port of Core.apply and the key check of
// Core.operate in core.py: an operator request needs the current key, a read with
// wait_seconds and a listen become waiting reads, and everything else goes to the
// Handler, whose rooms wake once it returns successfully.
func (server *server) dispatch(ctx context.Context, incoming wire.Incoming) (wire.Value, error) {
	now := server.settings.now()
	if incoming.Operator != nil {
		if !server.key.matches(incoming.Operator.OperatorKey()) {
			return wire.Value{}, Reject(wire.OperatorKeyRejected)
		}
		if operatorPing, isPing := incoming.Operator.(*wire.OperatorPing); isPing {
			return server.pingOperator(ctx, operatorPing, now)
		}
		return server.wakeAfter(server.handler.Operator(ctx, incoming.Operator, now))
	}
	if memberPing, isPing := incoming.Member.(*wire.Ping); isPing {
		return server.pingMember(ctx, memberPing, now)
	}
	if read, isRead := incoming.Member.(*wire.Read); isRead && read.WaitSeconds > 0 {
		return server.waitForRead(ctx, read)
	}
	if listen, isListen := incoming.Member.(*wire.Listen); isListen {
		return server.listen(ctx, listen)
	}
	return server.wakeAfter(server.handler.Member(ctx, incoming.Member, now))
}

// wakeAfter wakes the rooms and closes any revoked sessions of a successful outcome,
// returning its result.
func (server *server) wakeAfter(outcome Outcome, err error) (wire.Value, error) {
	if err != nil {
		return wire.Value{}, err
	}
	for _, target := range outcome.RevokeSessions {
		server.hub.closeSessions(target.Room, target.MemberID)
	}
	for _, room := range outcome.Notify {
		server.hub.notify(room)
	}
	for _, added := range outcome.Added {
		server.hub.notifyAdded(added)
	}
	return outcome.Result, nil
}
