package core

import (
	"bufio"
	"context"
	"net"
	"time"

	"github.com/witseko/agentcy/internal/wire"
)

// sessionPingTimeout bounds one on-demand ping across all open session streams of a
// member.
const sessionPingTimeout = 3 * time.Second

// maxSessionLineBytes bounds control lines on a session stream.
const maxSessionLineBytes = 1024

// tooManySessions is returned when a member or the daemon hits the session stream cap.
const tooManySessions = "Too many open session streams"

// sessionPeer represents one open shim MCP session registered with the hub.
type sessionPeer struct {
	room       Room
	memberID   int64
	connection net.Conn
	// probeLock is a 1-slot semaphore so waiting to probe respects ctx cancellation.
	probeLock chan struct{}
	pongs     chan bool
	closed    chan struct{}
}

// newSessionPeer builds a fully initialized sessionPeer for memberID in room.
func newSessionPeer(room Room, memberID int64, connection net.Conn) *sessionPeer {
	lock := make(chan struct{}, 1)
	lock <- struct{}{}
	return &sessionPeer{
		room:       room,
		memberID:   memberID,
		connection: connection,
		probeLock:  lock,
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

// probe sends one ping frame to the shim and waits until ctx expires for its MCP host
// pong, measuring RTT with now.
func (peer *sessionPeer) probe(ctx context.Context, draining <-chan struct{}, now func() time.Time) (bool, int64) {
	select {
	case <-peer.probeLock:
		defer func() { peer.probeLock <- struct{}{} }()
	case <-peer.closed:
		return false, 0
	case <-draining:
		return false, 0
	case <-ctx.Done():
		return false, 0
	}
	select {
	case <-peer.closed:
		return false, 0
	default:
	}
	peer.drainPongs()
	started := now()
	writeDeadline, hasDeadline := ctx.Deadline()
	if !hasDeadline {
		writeDeadline = time.Now().Add(sessionPingTimeout)
	}
	if err := peer.connection.SetWriteDeadline(writeDeadline); err != nil {
		return false, 0
	}
	if err := wire.WriteLine(peer.connection, []byte(`{"ping":true}`)); err != nil {
		return false, 0
	}
	_ = peer.connection.SetWriteDeadline(time.Time{})
	select {
	case ok := <-peer.pongs:
		if !ok {
			return false, 0
		}
		return true, max(0, now().Sub(started).Milliseconds())
	case <-peer.closed:
		return false, 0
	case <-draining:
		return false, 0
	case <-ctx.Done():
		return false, 0
	}
}

// serveSession authenticates a shim's session stream, holds it in the hub until the shim
// disconnects, its token is revoked, or the daemon drains, and answers probes on demand.
func (server *server) serveSession(ctx context.Context, connection net.Conn, request *wire.Session) {
	start, err := server.handler.OpenSession(ctx, request, server.settings.now())
	if err != nil {
		_ = connection.SetWriteDeadline(time.Now().Add(requestTimeout))
		_ = wire.WriteLine(connection, server.failureReply(wire.Incoming{Member: request}, err))
		return
	}
	peer := newSessionPeer(start.Room, start.MemberID, connection)
	if !server.hub.joinSession(peer) {
		_ = connection.SetWriteDeadline(time.Now().Add(requestTimeout))
		_ = wire.WriteLine(connection, errorReply(tooManySessions))
		return
	}
	defer server.hub.leaveSession(peer)
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

// probeResult carries one session peer's probe outcome.
type probeResult struct {
	alive bool
	rttMs int64
}

// probeSession probes every registered session stream of memberID in room concurrently
// under one shared sessionPingTimeout budget and returns as soon as any stream answers ok.
func (server *server) probeSession(ctx context.Context, room Room, memberID int64) (hasSession bool, alive bool, rttMs int64) {
	peers := server.hub.sessionPeers(room, memberID)
	if len(peers) == 0 {
		return false, false, 0
	}
	probeCtx, cancel := context.WithTimeout(ctx, sessionPingTimeout)
	defer cancel()
	results := make(chan probeResult, len(peers))
	for _, peer := range peers {
		go func(target *sessionPeer) {
			ok, elapsed := target.probe(probeCtx, server.draining, server.settings.now)
			results <- probeResult{alive: ok, rttMs: elapsed}
		}(peer)
	}
	for range peers {
		outcome := <-results
		if outcome.alive {
			cancel()
			return true, true, outcome.rttMs
		}
	}
	return true, false, 0
}

// pingMember authorizes a member ping and runs the live probe.
func (server *server) pingMember(ctx context.Context, request *wire.Ping, now time.Time) (wire.Value, error) {
	target, err := server.handler.ResolveMemberPing(ctx, request, now)
	if err != nil {
		return wire.Value{}, err
	}
	return server.finishPing(ctx, target), nil
}

// pingOperator authorizes an operator ping and runs the live probe.
func (server *server) pingOperator(ctx context.Context, request *wire.OperatorPing, now time.Time) (wire.Value, error) {
	target, err := server.handler.ResolveOperatorPing(ctx, request, now)
	if err != nil {
		return wire.Value{}, err
	}
	return server.finishPing(ctx, target), nil
}

// finishPing wakes any active waiting read for target and, when not listening, probes its
// open shim sessions outside any store transaction.
func (server *server) finishPing(ctx context.Context, target PingTarget) wire.Value {
	woke := server.hub.pingListener(target.Room, target.MemberID)
	var hasSession, sessionAlive bool
	var rttMs int64
	if !woke {
		hasSession, sessionAlive, rttMs = server.probeSession(ctx, target.Room, target.MemberID)
	}
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
	return result
}

// pingStatus picks the summary status label of one ping check.
func pingStatus(memberState string, woke, hasSession, sessionAlive bool) string {
	switch {
	case woke:
		return wire.PingStatusWoken
	case sessionAlive:
		return wire.PingStatusConnected
	case hasSession:
		return wire.PingStatusUnresponsive
	case memberState == "invited":
		return wire.PingStatusNotJoined
	default:
		return wire.PingStatusDisconnected
	}
}
