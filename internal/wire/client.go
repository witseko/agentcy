package wire

import (
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"syscall"
	"time"
)

// Size caps of the member protocol (protocol/README.md, "Member protocol").
const (
	MaxRequestBytes  = 128 * 1024
	MaxResponseBytes = 2 * 1024 * 1024
)

// The texts that the core and the shim agree on.
const (
	InvalidArguments = "Invalid bridge arguments"
	// coreVersionHelp follows an InvalidArguments rejection: the shim validated the
	// request, so the running core is likely another version.
	coreVersionHelp = "This shim built a valid request, so the running core is likely an older or newer " +
		"version. Stop the running core with 'make stop-core' in the agentcy clone, or stop the " +
		"process running 'agentcy core', and retry. The next call starts " +
		"the installed core. Join codes and conversations are kept."
)

// exchangeTimeout bounds each socket operation: the longest waiting read plus a margin.
const exchangeTimeout = (MaxWaitSeconds + 10) * time.Second

// ErrIncompleteResponse is returned when the core closes the socket before a full line.
var ErrIncompleteResponse = errors.New("Incomplete bridge response")

// ErrInvalidResponse is returned for a complete response line that is not a JSON object.
var ErrInvalidResponse = errors.New("Invalid bridge response")

// idempotentActions may be sent a second time after a transient disconnect.
var idempotentActions = map[string]bool{
	"read": true, "listen": true, "status": true, "plugins": true, "post": true, "close": true,
	"reopen": true, "release": true, "report_usage": true, "mcp_task_status": true,
	"mcp_task_cancel": true,
}

// CoreError is a rejection that the core sent as {"error": "..."}.
type CoreError struct {
	Message string
}

// Error returns the core's message.
func (rejection *CoreError) Error() string { return rejection.Message }

// Client sends member requests to the core over its Unix socket.
type Client struct {
	// Connect opens a connection to the core, starting it when needed.
	Connect func(ctx context.Context) (net.Conn, error)
}

// Exchange sends one request and returns the core's result. An idempotent request is
// sent once more after a broken pipe, a reset, or an incomplete response, unless ctx has
// already ended.
//
// Port of exchange in the former Python core's mcp/transport.py.
func (client *Client) Exchange(ctx context.Context, request Request) (Value, error) {
	result, err := client.exchangeOnce(ctx, request)
	if err != nil && ctx.Err() == nil && isTransientDisconnect(err) && idempotentActions[request.Action()] {
		return client.exchangeOnce(ctx, request)
	}
	return result, err
}

// isTransientDisconnect reports the failures that a retry may cure.
func isTransientDisconnect(err error) bool {
	return errors.Is(err, syscall.EPIPE) || errors.Is(err, syscall.ECONNRESET) || errors.Is(err, ErrIncompleteResponse)
}

// exchangeOnce writes one JSON line and reads one bounded JSON line back.
func (client *Client) exchangeOnce(ctx context.Context, request Request) (Value, error) {
	if err := ctx.Err(); err != nil {
		return Value{}, err
	}
	connection, err := client.Connect(ctx)
	if err != nil {
		return Value{}, err
	}
	defer connection.Close()
	stopCancel := context.AfterFunc(ctx, func() {
		_ = connection.Close()
	})
	defer stopCancel()
	if err := connection.SetDeadline(time.Now().Add(exchangeTimeout)); err != nil {
		return Value{}, err
	}
	if err := WriteLine(connection, []byte(Encode(request).Compact())); err != nil {
		if ctx.Err() != nil {
			return Value{}, ctx.Err()
		}
		return Value{}, err
	}
	line, err := ReadLine(connection, MaxResponseBytes)
	if err != nil {
		if ctx.Err() != nil {
			return Value{}, ctx.Err()
		}
		if errors.Is(err, io.EOF) || errors.Is(err, ErrLineTooLong) {
			return Value{}, ErrIncompleteResponse
		}
		return Value{}, err
	}
	return decodeResponse(line)
}

// decodeResponse returns the result of a response line or the core's rejection.
func decodeResponse(line []byte) (Value, error) {
	response, err := Parse(line)
	if err != nil || response.Kind != Object {
		return Value{}, ErrInvalidResponse
	}
	if rejection, found := response.Get("error"); found {
		message := rejection.StringOf()
		if strings.HasPrefix(message, InvalidArguments) {
			message += ". " + coreVersionHelp
		}
		return Value{}, &CoreError{Message: message}
	}
	result, _ := response.Get("result")
	return result, nil
}
