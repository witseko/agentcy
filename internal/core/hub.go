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
// Session stream caps prevent unbounded open sockets in the core.
const (
	maxSessionsPerMember = 8
	maxTotalSessions     = 256
)

// mutex guards rooms, sessions, totalSessions, and every listener's filter, skipped,
// settling, and pinged flags.
type hub struct {
	mutex         sync.Mutex
	rooms         map[Room]map[*listener]struct{}
	sessions      map[Room]map[*sessionPeer]struct{}
	totalSessions int
}

// listener is one waiting read's membership in a room.
type listener struct {
	hub      *hub
	room     Room
	memberID int64
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
	// pinged records an out-of-band ping that woke this listener without a message.
	pinged bool
}

// newHub returns an empty hub.
func newHub() *hub {
	return &hub{
		rooms:    map[Room]map[*listener]struct{}{},
		sessions: map[Room]map[*sessionPeer]struct{}{},
	}
}

// joinMember adds a listener for memberID to room. Call leave once when the wait ends.
func (hub *hub) joinMember(room Room, memberID int64) *listener {
	hub.mutex.Lock()
	defer hub.mutex.Unlock()
	listeners, found := hub.rooms[room]
	if !found {
		listeners = map[*listener]struct{}{}
		hub.rooms[room] = listeners
	}
	member := &listener{hub: hub, room: room, memberID: memberID, signal: make(chan struct{}, 1)}
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

// takePing reports and clears a pending out-of-band ping on listener.
func (listener *listener) takePing() bool {
	listener.hub.mutex.Lock()
	defer listener.hub.mutex.Unlock()
	pinged := listener.pinged
	listener.pinged = false
	return pinged
}

// pingListener wakes only the active waiting reads of memberID in room and reports
// whether at least one waiting read was woken.
func (hub *hub) pingListener(room Room, memberID int64) bool {
	if memberID == 0 {
		return false
	}
	hub.mutex.Lock()
	defer hub.mutex.Unlock()
	woke := false
	for member := range hub.rooms[room] {
		if member.memberID == memberID {
			member.pinged = true
			member.wake()
			woke = true
		}
	}
	return woke
}

// joinSession registers an active shim session stream if within the per-member and
// daemon-wide stream caps.
func (hub *hub) joinSession(peer *sessionPeer) bool {
	hub.mutex.Lock()
	defer hub.mutex.Unlock()
	if hub.totalSessions >= maxTotalSessions {
		return false
	}
	peers, found := hub.sessions[peer.room]
	if found {
		memberCount := 0
		for existing := range peers {
			if existing.memberID == peer.memberID {
				memberCount++
			}
		}
		if memberCount >= maxSessionsPerMember {
			return false
		}
	} else {
		peers = map[*sessionPeer]struct{}{}
		hub.sessions[peer.room] = peers
	}
	peers[peer] = struct{}{}
	hub.totalSessions++
	return true
}

// leaveSession removes a shim session stream and drops an empty room entry.
func (hub *hub) leaveSession(peer *sessionPeer) {
	hub.mutex.Lock()
	defer hub.mutex.Unlock()
	peers := hub.sessions[peer.room]
	if _, exists := peers[peer]; !exists {
		return
	}
	delete(peers, peer)
	hub.totalSessions--
	if len(peers) == 0 {
		delete(hub.sessions, peer.room)
	}
}

// closeSessions unregisters and closes every open session stream for memberID in room,
// or every stream in room when memberID is zero.
func (hub *hub) closeSessions(room Room, memberID int64) {
	hub.mutex.Lock()
	peers := hub.sessions[room]
	var toClose []*sessionPeer
	for peer := range peers {
		if memberID == 0 || peer.memberID == memberID {
			delete(peers, peer)
			hub.totalSessions--
			toClose = append(toClose, peer)
		}
	}
	if len(peers) == 0 {
		delete(hub.sessions, room)
	}
	hub.mutex.Unlock()
	for _, peer := range toClose {
		_ = peer.connection.Close()
	}
}

// sessionPeers returns a snapshot of the registered session streams for memberID in room.
func (hub *hub) sessionPeers(room Room, memberID int64) []*sessionPeer {
	if memberID == 0 {
		return nil
	}
	hub.mutex.Lock()
	defer hub.mutex.Unlock()
	var matched []*sessionPeer
	for peer := range hub.sessions[room] {
		if peer.memberID == memberID {
			matched = append(matched, peer)
		}
	}
	return matched
}

// presence snapshots the member IDs currently listening or connected in room.
func (hub *hub) presence(room Room) RoomPresence {
	hub.mutex.Lock()
	defer hub.mutex.Unlock()
	listeners := hub.rooms[room]
	peers := hub.sessions[room]
	if len(listeners) == 0 && len(peers) == 0 {
		return RoomPresence{}
	}
	result := RoomPresence{}
	if len(listeners) > 0 {
		result.listening = make(map[int64]bool, len(listeners))
		for member := range listeners {
			if member.memberID != 0 {
				result.listening[member.memberID] = true
			}
		}
	}
	if len(peers) > 0 {
		result.connected = make(map[int64]bool, len(peers))
		for peer := range peers {
			if peer.memberID != 0 {
				result.connected[peer.memberID] = true
			}
		}
	}
	return result
}
