// Package progress identifies the participants whose work is outstanding, from a
// conversation status. It is a port of src/agentcy/conversations/progress.py and holds
// pure functions only.
package progress

import "github.com/witseko/agentcy/internal/wire"

// The role labels that chat clients see.
const (
	RoleWorker = "worker"
	RoleHuman  = "human"
)

// PendingStates are the launch states of a fresh run that is still active
// (PENDING_STATES in src/agentcy/conversations/assignment_tables.py).
var PendingStates = []string{"queued", "starting", "running"}

// Task is one open task of a member.
type Task struct {
	ID      int64
	Blocked bool
	Overdue bool
}

// Member is one conversation member as the status reports it.
type Member struct {
	Name  string
	Role  string
	State string
	// LaunchState is the fresh-run state, nil when absent or null. HasLaunchState tells
	// whether the key was present at all, which one rule in LeadWaitingOn checks.
	LaunchState    *string
	HasLaunchState bool
	OpenTasks      []Task
	// IdleSeconds is nil when the status omits it.
	IdleSeconds *int64
}

// Status is the part of a status result that progress and guidance read. Members keep
// the order of the core's result, which decides the order of names in guidance.
type Status struct {
	Open              bool
	Participant       string
	Members           []Member
	Round             string
	ProposalSubmitted bool
	OpenInvite        bool
	// TokenBalance is the raw token_balance object, or a null value when absent.
	TokenBalance wire.Value
}

// Member returns the member with the given name and whether there is one.
func (status Status) Member(name string) (Member, bool) {
	for _, member := range status.Members {
		if member.Name == name {
			return member, true
		}
	}
	return Member{}, false
}

// StatusFromValue reads a status result. Missing fields take their zero value, and
// truthiness follows Python, as the Python shim reads them with dict.get.
func StatusFromValue(value wire.Value) Status {
	open, _ := value.Get("open")
	participant, _ := value.Get("participant")
	round, _ := value.Get("round")
	submitted, _ := value.Get("proposal_submitted")
	openInvite, _ := value.Get("open_invite")
	balance, found := value.Get("token_balance")
	if !found {
		balance = wire.NullValue()
	}
	status := Status{
		Open:              open.Truthy(),
		Participant:       participant.StringOf(),
		Round:             round.StringOf(),
		ProposalSubmitted: submitted.Truthy(),
		OpenInvite:        openInvite.Truthy(),
		TokenBalance:      balance,
	}
	members, _ := value.Get("members")
	for _, name := range members.Keys() {
		entry, _ := members.Get(name)
		status.Members = append(status.Members, memberFromValue(name, entry))
	}
	return status
}

// memberFromValue reads one member entry of a status result.
func memberFromValue(name string, entry wire.Value) Member {
	role, _ := entry.Get("role")
	state, _ := entry.Get("state")
	member := Member{Name: name, Role: role.StringOf(), State: state.StringOf()}
	if launch, found := entry.Get("launch_state"); found {
		member.HasLaunchState = true
		if launch.Kind == wire.String {
			text := launch.Text
			member.LaunchState = &text
		}
	}
	if idle, found := entry.Get("idle_seconds"); found {
		if seconds, ok := idle.IntOf(); ok {
			member.IdleSeconds = &seconds
		}
	}
	tasks, _ := entry.Get("open_tasks")
	for _, item := range tasks.Items {
		member.OpenTasks = append(member.OpenTasks, taskFromValue(item))
	}
	return member
}

// taskFromValue reads one open task.
func taskFromValue(item wire.Value) Task {
	id, _ := item.Get("id")
	blocked, _ := item.Get("blocked")
	overdue, _ := item.Get("overdue")
	number, _ := id.IntOf()
	return Task{ID: number, Blocked: blocked.Truthy(), Overdue: overdue.Truthy()}
}
