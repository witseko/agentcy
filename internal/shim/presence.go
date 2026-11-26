package shim

import (
	"bufio"
	"context"
	"net"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/witseko/agentcy/internal/wire"
)

// Presence timing bounds.
const (
	presenceAckTimeout   = 5 * time.Second
	presencePingTimeout  = 3 * time.Second
	presenceRetryBackoff = 2 * time.Second
	maxPresenceLineBytes = 1024
)

// SessionDialer opens a passive connection to the running core socket without spawning a
// new daemon process, so background reconnects never defeat the core's idle exit.
type SessionDialer func(ctx context.Context) (net.Conn, error)

// presenceKey uniquely identifies one MCP session and member token digest.
type presenceKey struct {
	session *mcp.ServerSession
	digest  string
}

// presenceTracker maintains one background session stream to the core per active stateful
// MCP session and member token, answering core ping probes via ServerSession.Ping.
type presenceTracker struct {
	workspace string
	dial      SessionDialer
	overHTTP  bool
	mutex     sync.Mutex
	active    map[presenceKey]context.CancelFunc
}

// newPresenceTracker returns a tracker when dial is set, or nil in unit tests without a
// socket dialer.
func newPresenceTracker(workspace string, dial SessionDialer, overHTTP bool) *presenceTracker {
	if dial == nil {
		return nil
	}
	return &presenceTracker{
		workspace: workspace,
		dial:      dial,
		overHTTP:  overHTTP,
		active:    map[presenceKey]context.CancelFunc{},
	}
}

// touch ensures a background session stream is running for the request's stateful MCP
// session and authenticated member token. Stateless 2026-07-28 HTTP requests are skipped.
func (tracker *presenceTracker) touch(request *mcp.CallToolRequest, token string) {
	if tracker == nil || request == nil || token == "" {
		return
	}
	if tracker.overHTTP && isModern(request) {
		return
	}
	session, ok := request.GetSession().(*mcp.ServerSession)
	if !ok || session == nil {
		return
	}
	key := presenceKey{session: session, digest: tokenDigest(token)}
	tracker.mutex.Lock()
	if _, running := tracker.active[key]; running {
		tracker.mutex.Unlock()
		return
	}
	if len(tracker.active) >= maxTrackedListeners {
		for existing, cancel := range tracker.active {
			cancel()
			delete(tracker.active, existing)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	tracker.active[key] = cancel
	tracker.mutex.Unlock()
	go tracker.runSession(ctx, cancel, key, token)
}

// runSession keeps one session stream open until the MCP session closes or the core
// permanently rejects the token.
func (tracker *presenceTracker) runSession(ctx context.Context, cancel context.CancelFunc, key presenceKey, token string) {
	defer func() {
		cancel()
		tracker.mutex.Lock()
		delete(tracker.active, key)
		tracker.mutex.Unlock()
	}()
	go func() {
		_ = key.session.Wait()
		cancel()
	}()
	for ctx.Err() == nil {
		accepted := tracker.streamOnce(ctx, key.session, token)
		if !accepted || ctx.Err() != nil {
			return
		}
		timer := time.NewTimer(presenceRetryBackoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

// streamOnce opens one session socket to the core, reads ping frames, and answers each
// with the result of ServerSession.Ping. It returns true when the core accepted the
// session before disconnecting (so a restart may be retried), and false on rejection.
func (tracker *presenceTracker) streamOnce(ctx context.Context, session *mcp.ServerSession, token string) bool {
	dialCtx, stopDial := context.WithTimeout(ctx, presenceAckTimeout)
	connection, err := tracker.dial(dialCtx)
	stopDial()
	if err != nil {
		return false
	}
	defer connection.Close()
	stopCancel := context.AfterFunc(ctx, func() {
		_ = connection.Close()
	})
	defer stopCancel()

	if err := connection.SetDeadline(time.Now().Add(presenceAckTimeout)); err != nil {
		return false
	}
	hello := wire.Encode(&wire.Session{Workspace: tracker.workspace, Token: token}).Compact()
	if err := wire.WriteLine(connection, []byte(hello)); err != nil {
		return false
	}
	reader := bufio.NewReaderSize(connection, maxPresenceLineBytes)
	ackLine, err := reader.ReadSlice('\n')
	if err != nil {
		return false
	}
	ack, err := wire.Parse(ackLine)
	if err != nil || ack.Kind != wire.Object {
		return false
	}
	if _, rejected := ack.Get("error"); rejected {
		return false
	}
	if err := connection.SetDeadline(time.Time{}); err != nil {
		return true
	}
	tracker.serveProbes(ctx, connection, reader, session)
	return true
}

// serveProbes reads {"ping":true} lines from reader and replies with {"pong":bool}.
func (tracker *presenceTracker) serveProbes(ctx context.Context, connection net.Conn, reader *bufio.Reader, session *mcp.ServerSession) {
	for ctx.Err() == nil {
		line, err := reader.ReadSlice('\n')
		if err != nil {
			return
		}
		frame, err := wire.Parse(line)
		if err != nil || frame.Kind != wire.Object {
			continue
		}
		pingFlag, found := frame.Get("ping")
		if !found || !pingFlag.Truthy() {
			continue
		}
		ok := pingHost(ctx, session)
		pong := `{"pong":false}`
		if ok {
			pong = `{"pong":true}`
		}
		_ = connection.SetWriteDeadline(time.Now().Add(presencePingTimeout))
		if err := wire.WriteLine(connection, []byte(pong)); err != nil {
			return
		}
		_ = connection.SetWriteDeadline(time.Time{})
	}
}

// pingHost sends one JSON-RPC ping from the MCP server session to the connected client.
func pingHost(ctx context.Context, session *mcp.ServerSession) bool {
	pingCtx, cancel := context.WithTimeout(ctx, presencePingTimeout)
	defer cancel()
	return session.Ping(pingCtx, &mcp.PingParams{}) == nil
}
