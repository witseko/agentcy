package core

import (
	"bufio"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/witseko/agentcy/internal/wire"
)

func TestPingWakesOnlyTargetListenerWithoutSettling(t *testing.T) {
	handler := &fakeHandler{
		openWait: func(request *wire.Read) (WaitStart, error) {
			if request.Token == "token-worker-1" {
				return WaitStart{Room: testRoom, MemberID: 10}, nil
			}
			return WaitStart{Room: testRoom, MemberID: 20}, nil
		},
		resolvePing: func(wire.Incoming) (PingTarget, error) {
			return PingTarget{Room: testRoom, MemberID: 10, Label: "Worker 1", State: "replied"}, nil
		},
	}
	daemon := startDaemon(t, handler, Options{SettleWindow: 5 * time.Second})

	firstReplies := make(chan string, 1)
	go func() {
		reply, _ := exchange(daemon.socket, []byte(line("action", "read", "workspace", testWorkspace, "token", "token-worker-1", "wait_seconds", 30)), false)
		firstReplies <- reply
	}()
	secondReplies := make(chan string, 1)
	go func() {
		reply, _ := exchange(daemon.socket, []byte(line("action", "read", "workspace", testWorkspace, "token", "token-worker-2", "wait_seconds", 1)), false)
		secondReplies <- reply
	}()
	if !eventually(t, 5*time.Second, func() bool {
		presence := daemon.server.hub.presence(testRoom)
		return presence.Listening(10) && presence.Listening(20)
	}) {
		t.Fatal("both waiting reads did not join the hub")
	}

	pingStarted := time.Now()
	pingReply := daemon.send(line("action", "ping", "workspace", testWorkspace, "token", testToken, "name", "Worker 1"), false)
	pingResult := resultOf(t, pingReply)
	if status, _ := pingResult.Get("status"); status.StringOf() != "woken" {
		t.Errorf("ping status %q, want woken (%s)", status.StringOf(), pingReply)
	}
	if woke, _ := pingResult.Get("woke"); !woke.Truthy() {
		t.Errorf("ping woke=%v, want true", woke)
	}

	firstReply := <-firstReplies
	if time.Since(pingStarted) > time.Second {
		t.Errorf("pinged read took %v, want immediate return without settle delay", time.Since(pingStarted))
	}
	pingedFlag, _ := resultOf(t, firstReply).Get("pinged")
	if !pingedFlag.Truthy() {
		t.Errorf("woken read missing pinged=true: %s", firstReply)
	}
	select {
	case unexpected := <-secondReplies:
		t.Errorf("other member's waiting read woke early on targeted ping: %s", unexpected)
	case <-time.After(150 * time.Millisecond):
	}
	<-secondReplies
}

func TestWaitingReadLeavesHubImmediatelyOnClientDisconnect(t *testing.T) {
	daemon := startDaemon(t, &fakeHandler{}, Options{})
	drop, err := dialAndSend(daemon.socket, readLine(45))
	if err != nil {
		t.Fatal(err)
	}
	waitForListener(t, daemon)
	droppedAt := time.Now()
	drop()
	if !eventually(t, time.Second, func() bool { return daemon.server.hub.roomCount() == 0 }) {
		t.Fatalf("listener remained in hub %v after client disconnect", time.Since(droppedAt))
	}
}

func TestSessionStreamReportsPresenceProbesAndCleansUpOnEOF(t *testing.T) {
	var seenConnected bool
	handler := &fakeHandler{
		openSession: func(*wire.Session) (WaitStart, error) {
			return WaitStart{Room: testRoom, MemberID: 42}, nil
		},
		resolvePing: func(wire.Incoming) (PingTarget, error) {
			idle := int64(180)
			return PingTarget{Room: testRoom, MemberID: 42, Label: "Worker", State: "replied", IdleSeconds: &idle}, nil
		},
		member: func(ctx context.Context, request wire.Request) (Outcome, error) {
			seenConnected = PresenceFromContext(ctx, testRoom).Connected(42)
			return resultNaming(request.Action()), nil
		},
	}
	daemon := startDaemon(t, handler, Options{})

	conn, err := dialUnix(daemon.socket)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := wire.WriteLine(conn, []byte(line("action", "session", "workspace", testWorkspace, "token", testToken))); err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(conn)
	ack, err := reader.ReadString('\n')
	if err != nil || !strings.Contains(ack, `"connected":true`) {
		t.Fatalf("session ack %q, err %v", ack, err)
	}

	// Answer one probe with pong=true, then answer the second probe with pong=false.
	go func() {
		probe1, err := reader.ReadString('\n')
		if err == nil && strings.Contains(probe1, `"ping":true`) {
			_ = wire.WriteLine(conn, []byte(`{"pong":true}`))
		}
		probe2, err := reader.ReadString('\n')
		if err == nil && strings.Contains(probe2, `"ping":true`) {
			_ = wire.WriteLine(conn, []byte(`{"pong":false}`))
		}
	}()

	daemon.send(statusLine(), false)
	if !seenConnected {
		t.Error("PresenceFromContext did not report connected=true while session stream was open")
	}

	key := daemon.operatorKeyText()
	opPing := line("version", 1, "key", key, "action", "operator_ping", "directory", "/tmp", "conversation", testRoomID, "name", "Worker")
	reply := resultOf(t, daemon.send(opPing, false))
	if status, _ := reply.Get("status"); status.StringOf() != "connected" {
		t.Errorf("first ping status %q, want connected (%s)", status.StringOf(), reply.Compact())
	}
	if _, hasRTT := reply.Get("ping_rtt_ms"); !hasRTT {
		t.Errorf("connected ping missing ping_rtt_ms: %s", reply.Compact())
	}

	reply2 := resultOf(t, daemon.send(opPing, false))
	if status, _ := reply2.Get("status"); status.StringOf() != "unresponsive" {
		t.Errorf("second ping status %q, want unresponsive (%s)", status.StringOf(), reply2.Compact())
	}

	_ = conn.Close()
	if !eventually(t, time.Second, func() bool { return !daemon.server.hub.presence(testRoom).Connected(42) }) {
		t.Fatal("session stayed connected after EOF")
	}
	reply3 := resultOf(t, daemon.send(opPing, false))
	if status, _ := reply3.Get("status"); status.StringOf() != "disconnected" {
		t.Errorf("post-EOF ping status %q, want disconnected (%s)", status.StringOf(), reply3.Compact())
	}
}

func TestOutcomeRevokeSessionsClosesStreams(t *testing.T) {
	handler := &fakeHandler{
		openSession: func(request *wire.Session) (WaitStart, error) {
			if request.Token == "token-member-1" {
				return WaitStart{Room: testRoom, MemberID: 10}, nil
			}
			return WaitStart{Room: testRoom, MemberID: 20}, nil
		},
		member: func(_ context.Context, request wire.Request) (Outcome, error) {
			outcome := resultNaming(request.Action())
			if request.Action() == "retire" {
				outcome.RevokeSessions = []SessionTarget{{Room: testRoom, MemberID: 10}}
			}
			if request.Action() == "close" {
				outcome.RevokeSessions = []SessionTarget{{Room: testRoom}}
			}
			return outcome, nil
		},
	}
	daemon := startDaemon(t, handler, Options{})

	open := func(token string) (*bufio.Reader, func()) {
		conn, err := dialUnix(daemon.socket)
		if err != nil {
			t.Fatal(err)
		}
		if err := wire.WriteLine(conn, []byte(line("action", "session", "workspace", testWorkspace, "token", token))); err != nil {
			t.Fatal(err)
		}
		reader := bufio.NewReader(conn)
		ack, err := reader.ReadString('\n')
		if err != nil || !strings.Contains(ack, `"connected":true`) {
			t.Fatalf("session ack %q, err %v", ack, err)
		}
		return reader, func() { _ = conn.Close() }
	}
	reader1, close1 := open("token-member-1")
	defer close1()
	reader2, close2 := open("token-member-2")
	defer close2()

	if !eventually(t, time.Second, func() bool {
		p := daemon.server.hub.presence(testRoom)
		return p.Connected(10) && p.Connected(20)
	}) {
		t.Fatal("both sessions did not register")
	}

	// Revoking member 10 closes only member 10's stream.
	daemon.send(line("action", "retire", "workspace", testWorkspace, "token", testToken, "name", "Worker"), false)
	if daemon.server.hub.presence(testRoom).Connected(10) {
		t.Error("member 10 remained connected after session revocation")
	}
	if !daemon.server.hub.presence(testRoom).Connected(20) {
		t.Error("member 20 lost connection when only member 10 was revoked")
	}
	if _, err := reader1.ReadString('\n'); err == nil {
		t.Error("member 10 stream did not see EOF after revocation")
	}

	// Revoking the whole room closes member 20's stream.
	daemon.send(line("action", "close", "workspace", testWorkspace, "token", testToken), false)
	if daemon.server.hub.presence(testRoom).Connected(20) {
		t.Error("member 20 remained connected after room revocation")
	}
	if _, err := reader2.ReadString('\n'); err == nil {
		t.Error("member 20 stream did not see EOF after room revocation")
	}
}

func TestPingSkipsSettleWindowWhenMessagesPending(t *testing.T) {
	calls := 0
	handler := &fakeHandler{
		openWait: func(*wire.Read) (WaitStart, error) {
			return WaitStart{Room: testRoom, MemberID: 10}, nil
		},
		readPage: func(*wire.Read) (wire.Value, error) {
			calls++
			if calls == 1 {
				return pageWith(0), nil
			}
			return pageWith(1), nil
		},
		resolvePing: func(wire.Incoming) (PingTarget, error) {
			return PingTarget{Room: testRoom, MemberID: 10, Label: "Worker", State: "replied"}, nil
		},
	}
	daemon := startDaemon(t, handler, Options{SettleWindow: 5 * time.Second})

	replies := make(chan string, 1)
	go func() {
		reply, _ := exchange(daemon.socket, []byte(line("action", "read", "workspace", testWorkspace, "token", testToken, "wait_seconds", 30)), false)
		replies <- reply
	}()
	if !eventually(t, time.Second, func() bool { return daemon.server.hub.presence(testRoom).Listening(10) }) {
		t.Fatal("waiting read did not join hub")
	}

	started := time.Now()
	daemon.send(line("action", "ping", "workspace", testWorkspace, "token", testToken, "name", "Worker"), false)
	reply := <-replies
	if time.Since(started) > time.Second {
		t.Fatalf("pinged ready read settled instead of returning immediately (%v)", time.Since(started))
	}
	result := resultOf(t, reply)
	if pinged, _ := result.Get("pinged"); !pinged.Truthy() {
		t.Errorf("pinged ready read missing pinged=true: %s", reply)
	}
}

func TestProbeSessionRunsConcurrentlyUnderSharedBudget(t *testing.T) {
	handler := &fakeHandler{
		openSession: func(*wire.Session) (WaitStart, error) {
			return WaitStart{Room: testRoom, MemberID: 42}, nil
		},
		resolvePing: func(wire.Incoming) (PingTarget, error) {
			return PingTarget{Room: testRoom, MemberID: 42, Label: "Worker", State: "replied"}, nil
		},
	}
	daemon := startDaemon(t, handler, Options{})

	// Open one unresponsive stream (never replies) and one responsive stream.
	slowConn, err := dialUnix(daemon.socket)
	if err != nil {
		t.Fatal(err)
	}
	defer slowConn.Close()
	if err := wire.WriteLine(slowConn, []byte(line("action", "session", "workspace", testWorkspace, "token", testToken))); err != nil {
		t.Fatal(err)
	}
	slowReader := bufio.NewReader(slowConn)
	if ack, err := slowReader.ReadString('\n'); err != nil || !strings.Contains(ack, `"connected":true`) {
		t.Fatalf("slow ack %q, err %v", ack, err)
	}

	fastConn, err := dialUnix(daemon.socket)
	if err != nil {
		t.Fatal(err)
	}
	defer fastConn.Close()
	if err := wire.WriteLine(fastConn, []byte(line("action", "session", "workspace", testWorkspace, "token", testToken))); err != nil {
		t.Fatal(err)
	}
	fastReader := bufio.NewReader(fastConn)
	if ack, err := fastReader.ReadString('\n'); err != nil || !strings.Contains(ack, `"connected":true`) {
		t.Fatalf("fast ack %q, err %v", ack, err)
	}
	go func() {
		if probe, err := fastReader.ReadString('\n'); err == nil && strings.Contains(probe, `"ping":true`) {
			_ = wire.WriteLine(fastConn, []byte(`{"pong":true}`))
		}
	}()

	started := time.Now()
	reply := resultOf(t, daemon.send(line("action", "ping", "workspace", testWorkspace, "token", testToken, "name", "Worker"), false))
	if time.Since(started) > time.Second {
		t.Fatalf("concurrent probe waited on slow stream (%v)", time.Since(started))
	}
	if status, _ := reply.Get("status"); status.StringOf() != wire.PingStatusConnected {
		t.Errorf("status = %q, want connected", status.StringOf())
	}
}
