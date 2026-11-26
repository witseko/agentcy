package core

import "sync"

// hub lets waiting reads sleep until a write to their room commits. Port of
// Notifications in concurrency.py, with targeted wakes (port plan fix L3).
//
// Each listener has its own signal channel with room for one wake, so a write that
// commits while the listener reads a page still ends its next sleep: no wake is lost, as
// with Python's condition held across the read. A room's entry exists only while it has
// listeners, like the Python WeakValueDictionary, so nothing grows with the number of
// rooms ever seen.
//
// A structural notice wakes every listener of the room. A notice of added messages wakes
// only the listeners whose filter says one of the messages makes their page ready; the
// others note that they skipped a change, which matters when their filter changes or
// they start to settle (see setFilter and startSettle).
//
// mutex guards rooms and every listener's filter and skipped flag.
type hub struct {
	mutex sync.Mutex
	rooms map[Room]map[*listener]struct{}
}

// listener is one waiting read's membership in a room.
type listener struct {
	hub  *hub
	room Room
	// signal holds at most one pending wake.
	signal chan struct{}
	// filter decides which added messages wake this listener; nil wakes it for any
	// added message.
	filter WakeFilter
	// skipped records an added message that did not wake the listener since its last page
	// read began.
	skipped bool
	// settling makes every added message wake the listener.
	settling bool
}

// newHub returns an empty hub.
func newHub() *hub {
	return &hub{rooms: map[Room]map[*listener]struct{}{}}
}

// join adds a listener to room. It wakes for every change until setFilter narrows it.
// Call leave exactly once when the wait ends.
func (hub *hub) join(room Room) *listener {
	hub.mutex.Lock()
	defer hub.mutex.Unlock()
	listeners, found := hub.rooms[room]
	if !found {
		listeners = map[*listener]struct{}{}
		hub.rooms[room] = listeners
	}
	member := &listener{hub: hub, room: room, signal: make(chan struct{}, 1)}
	listeners[member] = struct{}{}
	return member
}

// changed returns the channel that receives the listener's next wake.
func (listener *listener) changed() <-chan struct{} {
	return listener.signal
}

// beginRead clears the skipped flag. Call it just before each page read: a message
// skipped from here on may be missing from the page.
func (listener *listener) beginRead() {
	listener.hub.mutex.Lock()
	defer listener.hub.mutex.Unlock()
	listener.skipped = false
}

// setFilter replaces the listener's filter after a page read. When the filter changes
// (the reader's role changed) and a message was skipped under the old one, the listener
// wakes, because the new filter might have woken it.
func (listener *listener) setFilter(filter WakeFilter) {
	listener.hub.mutex.Lock()
	defer listener.hub.mutex.Unlock()
	if listener.skipped && filter != listener.filter {
		listener.wake()
	}
	listener.filter = filter
}

// startSettle makes every added message wake the listener from now on, and wakes it at
// once when it skipped a message during its last page read.
func (listener *listener) startSettle() {
	listener.hub.mutex.Lock()
	defer listener.hub.mutex.Unlock()
	listener.settling = true
	if listener.skipped {
		listener.wake()
	}
}

// wake leaves one pending wake. The caller holds the hub mutex.
func (listener *listener) wake() {
	select {
	case listener.signal <- struct{}{}:
	default:
	}
}

// leave removes the listener and drops the room's entry with its last listener.
func (listener *listener) leave() {
	hub := listener.hub
	hub.mutex.Lock()
	defer hub.mutex.Unlock()
	listeners := hub.rooms[listener.room]
	delete(listeners, listener)
	if len(listeners) == 0 {
		delete(hub.rooms, listener.room)
	}
}

// notify wakes every listener of room. A room without listeners needs nothing.
func (hub *hub) notify(room Room) {
	hub.mutex.Lock()
	defer hub.mutex.Unlock()
	for member := range hub.rooms[room] {
		member.wake()
	}
}

// notifyAdded wakes the listeners of a room that one of the added messages concerns and
// marks the others as having skipped a change. A write that added no message changed
// nothing a waiting read shows, so it wakes nobody.
func (hub *hub) notifyAdded(added AddedMessages) {
	if len(added.Messages) == 0 {
		return
	}
	hub.mutex.Lock()
	defer hub.mutex.Unlock()
	for member := range hub.rooms[added.Room] {
		if member.concerns(added.Messages) {
			member.wake()
		} else {
			member.skipped = true
		}
	}
}

// concerns reports whether one of messages wakes the listener. The caller holds the hub
// mutex.
func (listener *listener) concerns(messages []Message) bool {
	if listener.settling || listener.filter == nil {
		return true
	}
	for _, message := range messages {
		if listener.filter.Wakes(message) {
			return true
		}
	}
	return false
}
