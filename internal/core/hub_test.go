package core

import (
	"net"
	"testing"
)

// roomCount returns the number of rooms with listeners.
func (hub *hub) roomCount() int {
	hub.mutex.Lock()
	defer hub.mutex.Unlock()
	return len(hub.rooms)
}

// dialUnix connects to a socket path.
func dialUnix(socket string) (net.Conn, error) {
	return net.Dial("unix", socket)
}

// isClosed reports whether channel is closed without blocking.
func isClosed(channel <-chan struct{}) bool {
	select {
	case <-channel:
		return true
	default:
		return false
	}
}

func TestHubWakesEveryListenerOfTheRoomOnly(t *testing.T) {
	hub := newHub()
	other := Room{Workspace: testWorkspace, Conversation: "ffffffffffffffffffffffff"}
	first, second, elsewhere := hub.join(testRoom), hub.join(testRoom), hub.join(other)
	firstChanged, secondChanged, elsewhereChanged := first.changed(), second.changed(), elsewhere.changed()
	hub.notify(testRoom)
	if !isClosed(firstChanged) || !isClosed(secondChanged) {
		t.Error("a listener of the room was not woken")
	}
	if isClosed(elsewhereChanged) {
		t.Error("a listener of another room was woken")
	}
	if isClosed(first.changed()) {
		t.Error("the next wait would return without a new notify")
	}
}

func TestHubDropsARoomWithItsLastListener(t *testing.T) {
	hub := newHub()
	hub.notify(testRoom)
	if hub.roomCount() != 0 {
		t.Error("a notify without listeners created an entry")
	}
	first, second := hub.join(testRoom), hub.join(testRoom)
	first.leave()
	if hub.roomCount() != 1 {
		t.Error("the room was dropped while a listener remained")
	}
	second.leave()
	if hub.roomCount() != 0 {
		t.Error("the room outlived its last listener")
	}
}

func TestHubKeepsAWakeThatArrivesDuringARead(t *testing.T) {
	hub := newHub()
	listener := hub.join(testRoom)
	defer listener.leave()
	// The waiting read takes the channel before it reads the page; a commit and notify
	// during that read must still end the following wait.
	changed := listener.changed()
	hub.notify(testRoom)
	if !isClosed(changed) {
		t.Error("a wake during the read was lost")
	}
}
