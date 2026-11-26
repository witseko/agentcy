package core

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/witseko/agentcy/internal/wire"
)

// recipientFilter wakes for messages addressed to member only.
type recipientFilter struct {
	member int64
}

// Wakes reports a message addressed to the filter's member.
func (filter recipientFilter) Wakes(message Message) bool {
	return message.HasRecipient && message.Recipient == filter.member
}

// addressedTo returns an added message for recipient.
func addressedTo(recipient int64) AddedMessages {
	return AddedMessages{Room: testRoom, Messages: []Message{{ID: 9, Author: 1, Recipient: recipient, HasRecipient: true, Kind: "note"}}}
}

// skippedChange reports whether the listener skipped an added message since its last
// page read began.
func skippedChange(listener *listener) bool {
	listener.hub.mutex.Lock()
	defer listener.hub.mutex.Unlock()
	return listener.skipped
}

// woken reports whether the listener has a pending wake, and takes it.
func woken(listener *listener) bool {
	return isClosed(listener.changed())
}

func TestHubWakesOnlyTheListenersThatAnAddedMessageConcerns(t *testing.T) {
	hub := newHub()
	addressee, bystander, unfiltered := hub.join(testRoom), hub.join(testRoom), hub.join(testRoom)
	addressee.setFilter(recipientFilter{member: 2})
	bystander.setFilter(recipientFilter{member: 3})
	hub.notifyAdded(addressedTo(2))
	if !woken(addressee) || !woken(unfiltered) {
		t.Error("the addressee or a listener without a filter was not woken")
	}
	if woken(bystander) {
		t.Error("a listener that the message does not concern was woken")
	}
	if !skippedChange(bystander) || skippedChange(addressee) {
		t.Error("only the bystander should record a skipped message")
	}
	bystander.beginRead()
	if skippedChange(bystander) {
		t.Error("a new page read did not clear the skipped message")
	}
	hub.notify(testRoom)
	if !woken(addressee) || !woken(bystander) {
		t.Error("a structural notice did not wake every listener")
	}
}

func TestHubIgnoresAWriteThatAddedNoMessage(t *testing.T) {
	hub := newHub()
	listener := hub.join(testRoom)
	hub.notifyAdded(AddedMessages{Room: testRoom})
	if woken(listener) || skippedChange(listener) {
		t.Error("a write without messages woke or marked a listener")
	}
}

func TestHubWakesAListenerWhoseFilterChangedAfterASkip(t *testing.T) {
	hub := newHub()
	listener := hub.join(testRoom)
	listener.setFilter(recipientFilter{member: 3})
	listener.beginRead()
	// A role change lets the next read see the reader as someone else, while a message
	// was skipped under the old filter: the reader must read again.
	hub.notifyAdded(addressedTo(2))
	listener.setFilter(recipientFilter{member: 2})
	if !woken(listener) {
		t.Error("a skipped message under the old filter did not wake the listener")
	}
	listener.beginRead()
	listener.setFilter(recipientFilter{member: 2})
	if woken(listener) {
		t.Error("an unchanged filter woke the listener")
	}
}

func TestHubWakesASettlingListenerForEveryAddedMessage(t *testing.T) {
	hub := newHub()
	listener := hub.join(testRoom)
	listener.setFilter(recipientFilter{member: 3})
	listener.beginRead()
	hub.notifyAdded(addressedTo(2))
	listener.startSettle()
	if !woken(listener) {
		t.Error("a message skipped during the ready read did not wake the settling listener")
	}
	hub.notifyAdded(addressedTo(2))
	if !woken(listener) {
		t.Error("a settling listener slept through an added message")
	}
}

// targetedRoom is a fakeRoom whose posts announce one message for recipient 2, and whose
// pages are ready only when a message for the reader (recipient 3) arrived.
func targetedRoom(room *fakeRoom, forReader *atomic.Bool) *fakeHandler {
	handler := room.handler()
	handler.filter = recipientFilter{member: 3}
	handler.member = func(_ context.Context, request wire.Request) (Outcome, error) {
		room.mutex.Lock()
		room.messages++
		room.mutex.Unlock()
		recipient := int64(2)
		if forReader.Load() {
			recipient = 3
		}
		return Outcome{Result: resultNaming(request.Action()).Result, Added: []AddedMessages{addressedTo(recipient)}}, nil
	}
	read := handler.readPage
	handler.readPage = func(request *wire.Read) (wire.Value, error) {
		page, err := read(request)
		page.Set("wake", wire.BoolValue(forReader.Load()))
		return page, err
	}
	return handler
}

func TestWaitingReadSleepsThroughMessagesForOthers(t *testing.T) {
	room := &fakeRoom{}
	forReader := &atomic.Bool{}
	daemon := startDaemon(t, targetedRoom(room, forReader), Options{SettleWindow: 50 * time.Millisecond})
	replies := startWaitingRead(daemon, 1)
	waitForListener(t, daemon)
	daemon.send(postLine("fake-submission-1"), false)
	reply := <-replies
	// One read at the start and one when the deadline timer fires; the post woke nothing,
	// yet the returned page shows its message.
	if room.reads.Load() != 2 {
		t.Errorf("%d page reads, want 2", room.reads.Load())
	}
	if messageCount(t, reply) != 1 {
		t.Errorf("reply %q, want the message for another member on the last page", reply)
	}
}

func TestWaitingReadWakesForAMessageAddressedToIt(t *testing.T) {
	room := &fakeRoom{}
	forReader := &atomic.Bool{}
	daemon := startDaemon(t, targetedRoom(room, forReader), Options{SettleWindow: 50 * time.Millisecond})
	replies := startWaitingRead(daemon, 30)
	waitForListener(t, daemon)
	daemon.send(postLine("fake-submission-1"), false)
	forReader.Store(true)
	posted := time.Now()
	daemon.send(postLine("fake-submission-2"), false)
	reply := <-replies
	if messageCount(t, reply) != 2 || time.Since(posted) > 2*time.Second {
		t.Errorf("reply %q after %v, want both messages soon after the second post", reply, time.Since(posted))
	}
}

// A message for another member that fills the page ends the settle window at once, as
// any write did before targeted wakes.
func TestWaitingReadStopsSettlingWhenMessagesForOthersFillThePage(t *testing.T) {
	room := &fakeRoom{}
	handler := room.handler()
	handler.filter = recipientFilter{member: 3}
	// The first post is for the reader and makes the page ready; the second is for
	// another member and arrives while the read settles.
	handler.member = func(_ context.Context, request wire.Request) (Outcome, error) {
		room.mutex.Lock()
		room.messages++
		recipient := int64(2)
		if room.messages == 1 {
			recipient = 3
		}
		room.mutex.Unlock()
		return Outcome{Result: resultNaming(request.Action()).Result, Added: []AddedMessages{addressedTo(recipient)}}, nil
	}
	read := handler.readPage
	handler.readPage = func(request *wire.Read) (wire.Value, error) {
		page, err := read(request)
		page.Set("more", wire.BoolValue(page.Has("messages") && pageMessages(page).Len() >= 2))
		return page, err
	}
	daemon := startDaemon(t, handler, Options{SettleWindow: 5 * time.Second})
	replies := startWaitingRead(daemon, 30)
	waitForListener(t, daemon)
	daemon.send(postLine("fake-submission-1"), false)
	time.Sleep(50 * time.Millisecond)
	filled := time.Now()
	daemon.send(postLine("fake-submission-2"), false)
	reply := <-replies
	if messageCount(t, reply) != 2 || time.Since(filled) > time.Second {
		t.Errorf("reply %q after %v, want the full page at once", reply, time.Since(filled))
	}
}

func TestShutdownReturnsAPageWithASkippedMessage(t *testing.T) {
	room := &fakeRoom{}
	forReader := &atomic.Bool{}
	daemon := startDaemon(t, targetedRoom(room, forReader), Options{})
	replies := startWaitingRead(daemon, 45)
	waitForListener(t, daemon)
	daemon.send(postLine("fake-submission-1"), false)
	daemon.cancel()
	if reply := <-replies; messageCount(t, reply) != 1 {
		t.Errorf("reply %q, want the skipped message on the page returned at shutdown", reply)
	}
}

// listenLine is a valid listen request.
func listenLine(waitSeconds int) string {
	return line("action", "listen", "workspace", testWorkspace, "token", testToken, "wait_seconds", waitSeconds)
}

func TestListenReturnsThePageAndTheStatusInOneExchange(t *testing.T) {
	room := &fakeRoom{messages: 1}
	handler := room.handler()
	var delivered atomic.Bool
	handler.finishListen = func(page wire.Value, deliver bool) (wire.Value, error) {
		delivered.Store(deliver)
		return resultNaming("status").Result, nil
	}
	daemon := startDaemon(t, handler, Options{})
	result := resultOf(t, daemon.send(listenLine(30), false))
	page, _ := result.Get("page")
	status, _ := result.Get("status")
	if messages, _ := page.Get("messages"); messages.Len() != 1 {
		t.Errorf("page %s, want the ready message", page.Compact())
	}
	if handled, _ := status.Get("handled"); handled.StringOf() != "status" {
		t.Errorf("status %s, want the handler's status", status.Compact())
	}
	if !delivered.Load() || room.delivered.Load() != 0 {
		t.Error("the delivery was not recorded once, inside FinishListen")
	}
}

func TestListenNeedsAWait(t *testing.T) {
	daemon := startDaemon(t, &fakeHandler{}, Options{})
	if got := errorOf(t, daemon.send(listenLine(0), false)); got != wire.InvalidArguments {
		t.Errorf("error %q, want %q", got, wire.InvalidArguments)
	}
}
