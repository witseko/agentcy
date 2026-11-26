package core

import (
	"bufio"
	"context"
	"net"
	"sync"
	"time"

	"github.com/witseko/agentcy/internal/wire"
)

// sessionPingTimeout bounds one on-demand ping exchange between the core, a shim, and its
// MCP host.
const sessionPingTimeout = 3 * time.Second

// maxSessionLineBytes bounds control lines on a session stream.
const maxSessionLineBytes = 1024

// sessionPeer represents one open shim MCP session registered with the hub.
type sessionPeer struct {
	hub        *hub
	room       Room
	memberID   int64
	connection net.Conn
	probeMutex sync.Mutex
	pongs      chan bool
	closed     chan struct{}
}

// newSessionPeer builds a sessionPeer on connection.
func newSessionPeer(connection net.Conn) *sessionPeer {
	return &sessionPeer{
		connection: connection,
		pongs:      make(chan bool, 1),
		closed:     make(chan struct{}),
	}
}

// readLoop reads pong replies until connection closes or fails, then closes peer.closed.
func (peer *sessionPeer) readLoop() {
	defer close(peer.closed)
	reader := bufio.NewReaderSize(peer.connection, maxSessionLineBytes)
	for {
		line, err := reader.ReadSlice('\n')
		if err != nil {
			return
		}
		parsed, err := wire.Parse(line)
		if err != nil || parsed.Kind != wire.Object {
			continue
		}
		pong, found := parsed.Get("pong")
		if !found || pong.Kind != wire.Bool {
			continue
		}
		peer.deliverPong(pong.Boolean)
	}
}

// deliverPong stores the latest pong decision without blocking.
func (peer *sessionPeer) deliverPong(ok bool) {
	select {
	case <-peer.pongs:
	default:
	}
	select {
	case peer.pongs <- ok:
	default:
	}
}

// drainPongs clears any stale pong left by an earlier timed-out probe.
func (peer *sessionPeer) drainPongs() {
	select {
	case <-peer.pongs:
	default:
	}
}

// probe sends one ping frame to the shim and waits up to timeout for its MCP host pong.
func (peer *sessionPeer) probe(ctx context.Context, draining <-chan struct{}, timeout time.Duration) (bool, int64) {
	peer.probeMutex.Lock()
	defer peer.probeMutex.Unlock()
	select {
	case <-peer.closed:
		return false, 0
	default:
	}
	peer.drainPongs()
	started := time.Now()
	if err := peer.connection.SetWriteDeadline(started.Add(timeout)); err != nil {
		return false, 0
	}
	if err := wire.WriteLine(peer.connection, []byte(`{"ping":true}`)); err != nil {
		return false, 0
	}
	_ = peer.connection.SetWriteDeadline(time.Time{})
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case ok := <-peer.pongs:
		if !ok {
			return false, 0
		}
		return true, max(0, time.Since(started).Milliseconds())
	case <-peer.closed:
		return false, 0
	case <-draining:
		return false, 0
	case <-ctx.Done():
		return false, 0
	case <-timer.C:
		return false, 0
	}
}

// serveSession authenticates a shim's session stream, holds it in the hub until the shim
// disconnects or the daemon drains, and answers probes on demand.
func (server *server) serveSession(ctx context.Context, connection net.Conn, request *wire.Session) {
	start, err := server.handler.OpenSession(ctx, request, server.settings.now())
	if err != nil {
		_ = connection.SetWriteDeadline(time.Now().Add(requestTimeout))
		_ = wire.WriteLine(connection, server.failureReply(wire.Incoming{Member: request}, err))
		return
	}
	if err := connection.SetDeadline(time.Time{}); err != nil {
		return
	}
	if err := connection.SetWriteDeadline(time.Now().Add(requestTimeout)); err != nil {
		return
	}
	ack := wire.NewObject()
	ready := wire.NewObject()
	ready.Set("connected", wire.BoolValue(true))
	ack.Set("result", ready)
	if err := wire.WriteLine(connection, []byte(ack.Compact())); err != nil {
		return
	}
	_ = connection.SetWriteDeadline(time.Time{})

	peer := newSessionPeer(connection)
	server.hub.joinSession(start.Room, start.MemberID, peer)
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		peer.readLoop()
	}()
	select {
	case <-peer.closed:
	case <-server.draining:
	case <-ctx.Done():
	}
	server.hub.leaveSession(peer)
	_ = connection.Close()
	<-readerDone
}

// probeSession probes every registered session stream of memberID in room and reports
// whether a stream exists, whether any answered ok, and the fastest round-trip in ms.
func (server *server) probeSession(ctx context.Context, room Room, memberID int64) (hasSession bool, alive bool, rttMs int64) {
	peers := server.hub.sessionPeers(room, memberID)
	if len(peers) == 0 {
		return false, false, 0
	}
	for _, peer := range peers {
		ok, elapsed := peer.probe(ctx, server.draining, sessionPingTimeout)
		if ok && (!alive || elapsed < rttMs) {
			alive = true
			rttMs = elapsed
		}
	}
	return true, alive, rttMs
}

// ping resolves the target member in the store and then wakes its waiting read and probes
// its shim session outside any store transaction.
func (server *server) ping(ctx context.Context, incoming wire.Incoming) (wire.Value, error) {
	target, err := server.handler.ResolvePing(ctx, incoming, server.settings.now())
	if err != nil {
		return wire.Value{}, err
	}
	woke := server.hub.pingListener(target.Room, target.MemberID)
	hasSession, sessionAlive, rttMs := server.probeSession(ctx, target.Room, target.MemberID)
	connected := woke || sessionAlive
	status := pingStatus(target.State, woke, hasSession, sessionAlive)

	result := wire.NewObject()
	result.Set("participant", wire.TextValue(target.Label))
	result.Set("status", wire.TextValue(status))
	result.Set("listening", wire.BoolValue(woke))
	result.Set("connected", wire.BoolValue(connected))
	result.Set("woke", wire.BoolValue(woke))
	if sessionAlive {
		result.Set("ping_rtt_ms", wire.IntValue(rttMs))
	}
	if target.LaunchState != nil {
		result.Set("launch_state", wire.TextValue(*target.LaunchState))
	}
	if target.IdleSeconds != nil {
		result.Set("idle_seconds", wire.IntValue(*target.IdleSeconds))
	}
	return result, nil
}

// pingStatus picks the summary status label of one ping check.
func pingStatus(memberState string, woke, hasSession, sessionAlive bool) string {
	switch {
	case woke:
		return "woken"
	case sessionAlive:
		return "connected"
	case hasSession:
		return "unresponsive"
	case memberState == "invited":
		return "not_joined"
	default:
		return "disconnected"
	}
}
