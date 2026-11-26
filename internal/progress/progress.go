package progress

import "slices"

// isPending reports a launch state that keeps a fresh run active.
func isPending(launchState *string) bool {
	return launchState != nil && IsPendingState(*launchState)
}

// AwaitingFreshJoin recognizes a launch that still owns an unused invitation and can join.
func AwaitingFreshJoin(member Member) bool {
	return member.State == "invited" && isPending(member.LaunchState)
}

// IsRetired recognizes a member retired from the conversation.
func IsRetired(member Member) bool {
	return member.State == "retired"
}

// FreshRunEnded recognizes a launch that the bridge no longer tracks as active.
func FreshRunEnded(member Member) bool {
	return member.LaunchState != nil && !isPending(member.LaunchState)
}

// MemberIsWorking recognizes a peer who can still contribute through the current seat.
func MemberIsWorking(member Member) bool {
	if IsRetired(member) || FreshRunEnded(member) {
		return false
	}
	return member.State == "joined" || member.State == "replied" || AwaitingFreshJoin(member)
}

// TaskOwners returns the members with unfinished tasks, including blocked work, in
// status order.
func TaskOwners(status Status) []Member {
	var owners []Member
	for _, member := range status.Members {
		if len(member.OpenTasks) > 0 {
			owners = append(owners, member)
		}
	}
	return owners
}

// ownsTasks reports whether name is among the task owners.
func ownsTasks(owners []Member, name string) bool {
	for _, owner := range owners {
		if owner.Name == name {
			return true
		}
	}
	return false
}

// TaskAvailability returns the task owners who can work, in status order, and whether a
// live owner holds a blocked task.
//
// Python returns the owners as a set, whose iteration order varies between processes;
// this port keeps status order, which is one of the orders Python can produce.
func TaskAvailability(owners []Member) ([]string, bool) {
	var working []string
	blocked := false
	for _, member := range owners {
		if IsRetired(member) || FreshRunEnded(member) || (member.State == "invited" && !AwaitingFreshJoin(member)) {
			continue
		}
		for _, task := range member.OpenTasks {
			if task.Blocked {
				blocked = true
			} else if !slices.Contains(working, member.Name) {
				working = append(working, member.Name)
			}
		}
	}
	return working, blocked
}

// WorkingPeersWithoutTasks finds working peers whose progress is independent of the
// current task list.
func WorkingPeersWithoutTasks(members []Member, owners []Member, caller string) []string {
	var names []string
	for _, member := range members {
		if member.Name != caller && !ownsTasks(owners, member.Name) && MemberIsWorking(member) {
			names = append(names, member.Name)
		}
	}
	return names
}

// AvailableWorkers finds live peers who finished their work, hold no open tasks, and
// can take new work.
func AvailableWorkers(members []Member, owners []Member, caller string) []string {
	var names []string
	for _, member := range members {
		if member.Name != caller && member.Role != RoleHuman && !ownsTasks(owners, member.Name) &&
			member.State == "done" && !FreshRunEnded(member) {
			names = append(names, member.Name)
		}
	}
	return names
}

// Message is the part of a received message that blocker detection reads.
type Message struct {
	ID      int64
	Label   string
	Kind    string
	ReplyTo int64
}

// IsCurrentBlocker checks whether a received blocker still represents unfinished work.
//
// Python fails the whole read when the author is not a member; this port treats such a
// message as no current blocker.
func IsCurrentBlocker(message Message, members []Member, taskBlocker bool) bool {
	if message.Kind != "blocker" {
		return false
	}
	author, found := findMember(members, message.Label)
	if !found || IsRetired(author) || FreshRunEnded(author) {
		return false
	}
	if !taskBlocker {
		return author.State == "blocked"
	}
	for _, task := range author.OpenTasks {
		if task.ID == message.ReplyTo && task.Blocked {
			return true
		}
	}
	return false
}

// findMember returns the member with the given name.
func findMember(members []Member, name string) (Member, bool) {
	for _, member := range members {
		if member.Name == name {
			return member, true
		}
	}
	return Member{}, false
}

// QuietPeer is the working peer with the longest recorded silence.
type QuietPeer struct {
	Name        string
	IdleSeconds int64
	TaskIDs     []int64
}

// QuietestWorkingPeer identifies the working peer with the longest recorded silence and
// its unblocked open task IDs. The first candidate wins a tie.
func QuietestWorkingPeer(members []Member, candidates []string, owners []Member) (QuietPeer, bool) {
	var best QuietPeer
	found := false
	for _, name := range candidates {
		member, ok := findMember(members, name)
		if !ok || !MemberIsWorking(member) || member.IdleSeconds == nil {
			continue
		}
		if found && *member.IdleSeconds <= best.IdleSeconds {
			continue
		}
		best = QuietPeer{Name: name, IdleSeconds: *member.IdleSeconds, TaskIDs: unblockedTaskIDs(owners, name)}
		found = true
	}
	return best, found
}

// unblockedTaskIDs lists the unblocked open task IDs of one owner.
func unblockedTaskIDs(owners []Member, name string) []int64 {
	var ids []int64
	owner, found := findMember(owners, name)
	if !found {
		return ids
	}
	for _, task := range owner.OpenTasks {
		if !task.Blocked {
			ids = append(ids, task.ID)
		}
	}
	return ids
}

// BlockedPeer is another active peer who is blocked, with its blocked task IDs.
type BlockedPeer struct {
	Name  string
	Tasks []int64
}

// BlockedPeers lists other active peers who are blocked so teammates can offer help.
func BlockedPeers(members []Member, caller string) []BlockedPeer {
	var peers []BlockedPeer
	for _, member := range members {
		if member.Name == caller || member.Role == RoleHuman || IsRetired(member) ||
			FreshRunEnded(member) || member.State != "blocked" {
			continue
		}
		peer := BlockedPeer{Name: member.Name}
		for _, task := range member.OpenTasks {
			if task.Blocked {
				peer.Tasks = append(peer.Tasks, task.ID)
			}
		}
		peers = append(peers, peer)
	}
	return peers
}

// HasActivePeers checks whether any other non-human member is working or starting.
func HasActivePeers(members []Member, caller string) bool {
	for _, member := range members {
		if member.Name != caller && member.Role != RoleHuman && MemberIsWorking(member) {
			return true
		}
	}
	return false
}

// LeadWaitingOn lists the most urgent group of peers with incomplete work. A worker
// waits on nobody.
func LeadWaitingOn(status Status, caller string) []string {
	if me, _ := status.Member(caller); me.Role == RoleWorker {
		return nil
	}
	owners := TaskOwners(status)
	if len(owners) > 0 {
		if pending := pendingOwners(status, owners, caller); len(pending) > 0 {
			return pending
		}
	}
	var working, blocked, invited []string
	for _, member := range status.Members {
		if member.Name == caller || IsRetired(member) || FreshRunEnded(member) {
			continue
		}
		switch {
		case MemberIsWorking(member):
			working = append(working, member.Name)
		case member.State == "blocked":
			blocked = append(blocked, member.Name)
		case member.State == "invited" && !member.HasLaunchState:
			invited = append(invited, member.Name)
		}
	}
	return firstNonEmpty(working, blocked, invited)
}

// pendingOwners picks, among task owners other than the caller, those who can work,
// else the working peers without tasks, else every live owner.
func pendingOwners(status Status, owners []Member, caller string) []string {
	workingOwners, _ := TaskAvailability(owners)
	var live, working []string
	for _, owner := range owners {
		if owner.Name == caller || IsRetired(owner) || FreshRunEnded(owner) {
			continue
		}
		live = append(live, owner.Name)
		if slices.Contains(workingOwners, owner.Name) {
			working = append(working, owner.Name)
		}
	}
	return firstNonEmpty(working, WorkingPeersWithoutTasks(status.Members, owners, caller), live)
}

// firstNonEmpty returns the first list that holds a name, or nil.
func firstNonEmpty(lists ...[]string) []string {
	for _, list := range lists {
		if len(list) > 0 {
			return list
		}
	}
	return nil
}
