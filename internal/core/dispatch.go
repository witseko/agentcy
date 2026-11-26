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
		if _, isPing := incoming.Operator.(*wire.OperatorPing); isPing {
			return server.ping(ctx, incoming)
		}
		return server.wakeAfter(server.handler.Operator(ctx, incoming.Operator, now))
	}
	if _, isPing := incoming.Member.(*wire.Ping); isPing {
		return server.ping(ctx, incoming)
	}
	if read, isRead := incoming.Member.(*wire.Read); isRead && read.WaitSeconds > 0 {
		return server.waitForRead(ctx, read)
	}
	if listen, isListen := incoming.Member.(*wire.Listen); isListen {
		return server.listen(ctx, listen)
	}
	return server.wakeAfter(server.handler.Member(ctx, incoming.Member, now))
}

// wakeAfter wakes the rooms of a successful outcome and returns its result.
func (server *server) wakeAfter(outcome Outcome, err error) (wire.Value, error) {
	if err != nil {
		return wire.Value{}, err
	}
	for _, room := range outcome.Notify {
		server.hub.notify(room)
	}
	for _, added := range outcome.Added {
		server.hub.notifyAdded(added)
	}
	return outcome.Result, nil
}
