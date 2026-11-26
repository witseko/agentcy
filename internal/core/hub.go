package core

import "sync"

// hub lets waiting reads sleep until a write to their room commits. Port of
// Notifications in concurrency.py.
//
// Each room with at least one listener has a channel that a notify closes and replaces.
// A listener takes the current channel before it reads a page, so a write that commits
// during the read still wakes it: no wake is lost, as with Python's condition held across
// the read. A room's entry exists only while it has listeners, like the Python
// WeakValueDictionary, so nothing grows with the number of rooms ever seen.
type hub struct {
	mutex sync.Mutex
	rooms map[Room]*roomSignal
}

// roomSignal is the wake channel of one room and the number of its listeners.
type roomSignal struct {
	changed   chan struct{}
	listeners int
}

// listener is one waiting read's membership in a room.
type listener struct {
	hub  *hub
	room Room
}

// newHub returns an empty hub.
func newHub() *hub {
	return &hub{rooms: map[Room]*roomSignal{}}
}

// join adds a listener to room. Call leave exactly once when the wait ends.
func (hub *hub) join(room Room) *listener {
	hub.mutex.Lock()
	defer hub.mutex.Unlock()
	signal, found := hub.rooms[room]
	if !found {
		signal = &roomSignal{changed: make(chan struct{})}
		hub.rooms[room] = signal
	}
	signal.listeners++
	return &listener{hub: hub, room: room}
}

// changed returns the channel that the next notify of the listener's room closes.
func (listener *listener) changed() <-chan struct{} {
	listener.hub.mutex.Lock()
	defer listener.hub.mutex.Unlock()
	return listener.hub.rooms[listener.room].changed
}

// leave removes the listener and drops the room's entry with its last listener.
func (listener *listener) leave() {
	hub := listener.hub
	hub.mutex.Lock()
	defer hub.mutex.Unlock()
	signal := hub.rooms[listener.room]
	signal.listeners--
	if signal.listeners == 0 {
		delete(hub.rooms, listener.room)
	}
}

// notify wakes every listener of room. A room without listeners needs nothing.
func (hub *hub) notify(room Room) {
	hub.mutex.Lock()
	defer hub.mutex.Unlock()
	if signal, found := hub.rooms[room]; found {
		close(signal.changed)
		signal.changed = make(chan struct{})
	}
}
