package core

import (
	"context"

	"github.com/witseko/agentcy/internal/wire"
)

// dispatch routes one validated request. Port of Core.apply and the key check of
// Core.operate in core.py: an operator request needs the current key, a read with
// wait_seconds becomes a waiting read, and everything else goes to the Handler, whose
// rooms wake once it returns successfully.
func (server *server) dispatch(ctx context.Context, incoming wire.Incoming) (wire.Value, error) {
	now := server.settings.now()
	if incoming.Operator != nil {
		if !server.key.matches(incoming.Operator.OperatorKey()) {
			return wire.Value{}, Reject(wire.OperatorKeyRejected)
		}
		return server.wakeAfter(server.handler.Operator(ctx, incoming.Operator, now))
	}
	if read, isRead := incoming.Member.(*wire.Read); isRead && read.WaitSeconds > 0 {
		return server.waitForRead(ctx, read)
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
	return outcome.Result, nil
}
