package core

import (
	"bufio"
	"context"
	"errors"
	"io"
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
	line, err := readRequestLine(connection)
	if err != nil || len(line) == 0 {
		return
	}
	reply := server.answer(ctx, line)
	if err := connection.SetWriteDeadline(time.Now().Add(requestTimeout)); err != nil {
		return
	}
	// The client may be gone; there is nobody left to tell.
	_, _ = connection.Write(reply)
}

// readRequestLine reads up to and including the first newline, stopping after
// MaxRequestBytes+1 bytes like readline(MAX_REQUEST + 1). An end of stream is not an
// error: the caller sees a short or unterminated line.
func readRequestLine(connection io.Reader) ([]byte, error) {
	reader := bufio.NewReader(io.LimitReader(connection, wire.MaxRequestBytes+1))
	line, err := reader.ReadBytes('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	return line, nil
}

// answer turns one request line into one reply line.
func (server *server) answer(ctx context.Context, line []byte) []byte {
	if len(line) > wire.MaxRequestBytes || line[len(line)-1] != '\n' {
		return errorReply(requestTooLarge)
	}
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
