package core

import (
	"context"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/witseko/agentcy/internal/wire"
)

// fakeRoom is a conversation whose posts wake waiting reads, as the store would.
type fakeRoom struct {
	mutex     sync.Mutex
	messages  int
	more      bool
	reads     atomic.Int32
	delivered atomic.Int32
}

// handler returns a fakeHandler on this room: a "post" adds a message and wakes the room,
// and a page shows every message.
func (room *fakeRoom) handler() *fakeHandler {
	return &fakeHandler{
		member: func(_ context.Context, request wire.Request) (Outcome, error) {
			if request.Action() != "post" {
				return resultNaming(request.Action()), nil
			}
			room.mutex.Lock()
			room.messages++
			room.mutex.Unlock()
			return Outcome{Result: resultNaming("post").Result, Notify: []Room{testRoom}}, nil
		},
		readPage: func(*wire.Read) (wire.Value, error) {
			room.reads.Add(1)
			room.mutex.Lock()
			defer room.mutex.Unlock()
			page := pageWith(room.messages)
			if room.more {
				page.Set("more", wire.BoolValue(true))
			}
			return page, nil
		},
		recordDelivery: func(wire.Value) error {
			room.delivered.Add(1)
			return nil
		},
	}
}

// postLine is a valid post request.
func postLine(submission string) string {
	return line("action", "post", "workspace", testWorkspace, "token", testToken, "submission_id", submission, "body", "Fake message.")
}

// startWaitingRead sends a waiting read in the background and returns its reply channel.
func startWaitingRead(daemon *testDaemon, waitSeconds int) <-chan string {
	replies := make(chan string, 1)
	go func() {
		reply, _ := exchange(daemon.socket, []byte(readLine(waitSeconds)), false)
		replies <- reply
	}()
	return replies
}

// waitForListener waits until the hub has a listener, so a wake cannot come too early.
func waitForListener(t *testing.T, daemon *testDaemon) {
	t.Helper()
	if !eventually(t, 5*time.Second, func() bool { return daemon.server.hub.roomCount() == 1 }) {
		t.Fatal("the waiting read never joined its room")
	}
}

// messageCount returns the number of messages in a read reply.
func messageCount(t *testing.T, reply string) int {
	t.Helper()
	messages, _ := resultOf(t, reply).Get("messages")
	return messages.Len()
}

func TestWaitingReadReturnsAReadyPageAtOnce(t *testing.T) {
	room := &fakeRoom{messages: 1}
	daemon := startDaemon(t, room.handler(), Options{})
	started := time.Now()
	reply := daemon.send(readLine(30), false)
	if messageCount(t, reply) != 1 || time.Since(started) > time.Second {
		t.Errorf("reply %q after %v, want one message at once", reply, time.Since(started))
	}
	if room.reads.Load() != 1 || room.delivered.Load() != 1 {
		t.Errorf("%d reads and %d usage records, want 1 and 1", room.reads.Load(), room.delivered.Load())
	}
}

func TestWaitingReadWakesOnAPostAndSettlesTheBurst(t *testing.T) {
	room := &fakeRoom{}
	settle := 400 * time.Millisecond
	daemon := startDaemon(t, room.handler(), Options{SettleWindow: settle})
	replies := startWaitingRead(daemon, 30)
	waitForListener(t, daemon)
	firstPost := time.Now()
	daemon.send(postLine("fake-submission-1"), false)
	time.Sleep(100 * time.Millisecond)
	daemon.send(postLine("fake-submission-2"), false)
	reply := <-replies
	elapsed := time.Since(firstPost)
	if got := messageCount(t, reply); got != 2 {
		t.Errorf("settled page has %d messages, want the burst of 2", got)
	}
	if elapsed < settle || elapsed > settle+time.Second {
		t.Errorf("read returned %v after the first post, want about the settle window %v", elapsed, settle)
	}
	if room.delivered.Load() != 1 {
		t.Errorf("%d usage records, want 1", room.delivered.Load())
	}
}

func TestWaitingReadStopsSettlingOnAFullPage(t *testing.T) {
	room := &fakeRoom{more: true}
	daemon := startDaemon(t, room.handler(), Options{SettleWindow: 5 * time.Second})
	replies := startWaitingRead(daemon, 30)
	waitForListener(t, daemon)
	posted := time.Now()
	daemon.send(postLine("fake-submission-1"), false)
	reply := <-replies
	if messageCount(t, reply) != 1 || time.Since(posted) > time.Second {
		t.Errorf("full page returned after %v, want at once", time.Since(posted))
	}
}

func TestWaitingReadIgnoresWakesOfOtherRooms(t *testing.T) {
	room := &fakeRoom{}
	handler := room.handler()
	handler.member = func(context.Context, wire.Request) (Outcome, error) {
		other := Room{Workspace: testWorkspace, Conversation: "ffffffffffffffffffffffff"}
		return Outcome{Result: resultNaming("post").Result, Notify: []Room{other}}, nil
	}
	daemon := startDaemon(t, handler, Options{})
	replies := startWaitingRead(daemon, 1)
	waitForListener(t, daemon)
	daemon.send(postLine("fake-submission-1"), false)
	<-replies
	if room.reads.Load() != 2 {
		t.Errorf("%d page reads, want 2: the first and the one at the deadline", room.reads.Load())
	}
}

func TestWaitingReadTimesOutWithTheLastPage(t *testing.T) {
	room := &fakeRoom{}
	daemon := startDaemon(t, room.handler(), Options{})
	started := time.Now()
	reply := daemon.send(readLine(1), false)
	elapsed := time.Since(started)
	if messageCount(t, reply) != 0 {
		t.Errorf("timeout reply %q, want an empty page", reply)
	}
	if elapsed < time.Second || elapsed > 2500*time.Millisecond {
		t.Errorf("timed out after %v, want about 1 s", elapsed)
	}
	if room.delivered.Load() != 0 {
		t.Error("usage recorded for a page without messages")
	}
}

func TestWaitingReadWakesAtTheSealedRoundDeadline(t *testing.T) {
	roundDeadline := time.Now().Unix() + 1
	handler := &fakeHandler{
		openWait: func(*wire.Read) (WaitStart, error) {
			return WaitStart{Room: testRoom, RoundDeadline: &roundDeadline}, nil
		},
		readPage: func(*wire.Read) (wire.Value, error) {
			page := pageWith(0)
			if time.Now().Unix() < roundDeadline {
				page.Set("round_deadline", wire.IntValue(roundDeadline))
				return page, nil
			}
			// The store released the round: the page wakes the reader and drops the deadline.
			page.Set("wake", wire.BoolValue(true))
			return page, nil
		},
	}
	daemon := startDaemon(t, handler, Options{SettleWindow: 50 * time.Millisecond})
	started := time.Now()
	reply := daemon.send(readLine(30), false)
	wake, _ := resultOf(t, reply).Get("wake")
	if !wake.Truthy() || time.Since(started) > 3*time.Second {
		t.Errorf("reply %q after %v, want a wake at the round deadline within about 1 s", reply, time.Since(started))
	}
}

func TestWaitingReadsLeaveNoGoroutineOrRoomBehind(t *testing.T) {
	room := &fakeRoom{}
	daemon := startDaemon(t, room.handler(), Options{SettleWindow: 50 * time.Millisecond})
	baseline := runtime.NumGoroutine()
	// One read times out, one is woken, and one loses its client before the deadline.
	timedOut := startWaitingRead(daemon, 1)
	woken := startWaitingRead(daemon, 30)
	dropped, err := dialAndSend(daemon.socket, readLine(1))
	if err != nil {
		t.Fatal(err)
	}
	dropped()
	if !eventually(t, 5*time.Second, func() bool { return room.reads.Load() >= 3 }) {
		t.Fatal("the reads did not start")
	}
	daemon.send(postLine("fake-submission-1"), false)
	<-woken
	<-timedOut
	clean := eventually(t, 5*time.Second, func() bool {
		return daemon.server.hub.roomCount() == 0 && runtime.NumGoroutine() <= baseline
	})
	if !clean {
		t.Errorf("%d rooms and %d goroutines left, want 0 rooms and at most %d goroutines",
			daemon.server.hub.roomCount(), runtime.NumGoroutine(), baseline)
	}
}

func TestShutdownEndsWaitingReadsAtOnce(t *testing.T) {
	room := &fakeRoom{}
	daemon := startDaemon(t, room.handler(), Options{})
	replies := startWaitingRead(daemon, 45)
	waitForListener(t, daemon)
	stoppedAt := time.Now()
	daemon.cancel()
	reply := <-replies
	if messageCount(t, reply) != 0 || time.Since(stoppedAt) > time.Second {
		t.Errorf("reply %q after %v, want the current page at once", reply, time.Since(stoppedAt))
	}
	if err := daemon.wait(5 * time.Second); err != nil {
		t.Fatal(err)
	}
}

// dialAndSend writes one line on a new connection and returns a function that drops it.
func dialAndSend(socket, payload string) (func(), error) {
	connection, err := dialUnix(socket)
	if err != nil {
		return nil, err
	}
	if _, err := connection.Write([]byte(payload)); err != nil {
		connection.Close()
		return nil, err
	}
	return func() { connection.Close() }, nil
}
