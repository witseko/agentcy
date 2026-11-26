package core

import (
	"context"
	"errors"
	"net"
	"time"

	"github.com/witseko/agentcy/internal/wire"
)

// The error texts that the daemon itself sends.
const (
	requestTooLarge     = "Request exceeds size limit"
	coreOperationFailed = "Core operation failed"
)

// serveConnection answers one request on one connection. Port of Handler.handle in
// core.py:
//
//   - It reads one line of at most 128 KiB including its newline, within 5 seconds.
//   - A timeout, a reset, or an empty connection that closes gets no reply.
//   - A longer line, or a line that ends without a newline, gets "Request exceeds size
//     limit". Bytes after the first newline are ignored.
//   - The reply is one JSON line, {"result": ...} or {"error": "..."}, written within 5
//     seconds.
func (server *server) serveConnection(ctx context.Context, connection net.Conn) {
	defer connection.Close()
	if err := connection.SetReadDeadline(time.Now().Add(requestTimeout)); err != nil {
		return
	}
	line, err := wire.ReadLine(connection, wire.MaxRequestBytes)
	var reply []byte
	switch {
	case errors.Is(err, wire.ErrLineTooLong):
		reply = errorReply(requestTooLarge)
	case err != nil:
		return
	default:
		reply = server.answer(ctx, line)
	}
	if err := connection.SetWriteDeadline(time.Now().Add(requestTimeout)); err != nil {
		return
	}
	// The client may be gone; there is nobody left to tell.
	_ = wire.WriteLine(connection, reply)
}

// answer turns one validated request line into one reply line.
func (server *server) answer(ctx context.Context, line []byte) []byte {
	incoming, err := wire.DecodeIncoming(line)
	if errors.Is(err, wire.ErrOperatorKeyRejected) {
		return errorReply(wire.OperatorKeyRejected)
	}
	if err != nil {
		return errorReply(wire.InvalidArguments)
	}
	result, err := server.dispatchSafely(ctx, incoming)
	if err != nil {
		return server.failureReply(incoming, err)
	}
	reply := wire.NewObject()
	reply.Set("result", result)
	encoded := []byte(reply.Compact() + "\n")
	if len(encoded) > wire.MaxResponseBytes {
		// The shim would refuse the line anyway; a short error tells it why the call failed.
		server.settings.logger.Printf("%s: reply exceeds size limit (action %s)", coreOperationFailed, actionOf(incoming))
		return errorReply(coreOperationFailed)
	}
	return encoded
}

// dispatchSafely runs dispatch and turns a panic into an error, so one bad request cannot
// stop the daemon.
func (server *server) dispatchSafely(ctx context.Context, incoming wire.Incoming) (result wire.Value, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = errHandlerPanic
		}
	}()
	return server.dispatch(ctx, incoming)
}

// errHandlerPanic stands in for a recovered panic, whose value may hold request data.
var errHandlerPanic = errors.New("handler panic")

// failureReply sends a Rejection's message and hides every other error. Only the action
// name is logged: an error may carry paths, SQL, or request data.
func (server *server) failureReply(incoming wire.Incoming, err error) []byte {
	var rejection *Rejection
	if errors.As(err, &rejection) {
		return errorReply(rejection.Message)
	}
	server.settings.logger.Printf("%s (action %s)", coreOperationFailed, actionOf(incoming))
	return errorReply(coreOperationFailed)
}

// errorReply encodes {"error": message} as one line.
func errorReply(message string) []byte {
	reply := wire.NewObject()
	reply.Set("error", wire.TextValue(message))
	return []byte(reply.Compact() + "\n")
}

// actionOf returns the action name of a decoded request, which is safe to log.
func actionOf(incoming wire.Incoming) string {
	if incoming.Operator != nil {
		return incoming.Operator.Action()
	}
	return incoming.Member.Action()
}
