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
