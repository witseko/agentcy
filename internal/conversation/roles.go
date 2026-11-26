package conversation

// The participant roles and their stored values (port of roles.py). The values keep the
// original worker and creator numbers of old databases.
const (
	roleWorker = 0
	roleLead   = 1
	roleCoLead = 2
	// roleHuman is the person at the operator protocol. The row has no token and no
	// invitation.
	roleHuman = 3
)

// humanLabel is the member name of the human. Models cannot use it, so a message from it
// always comes from the operator protocol.
const humanLabel = "Human"

// humanAlias is the word the terminal UI shows for the human, next to their member name.
const humanAlias = "You"

// roleLabel returns the role name used by chat clients, such as "co-lead".
func roleLabel(role int64) string {
	switch role {
	case roleWorker:
		return "worker"
	case roleLead:
		return "lead"
	case roleCoLead:
		return "co-lead"
	case roleHuman:
		return "human"
	}
	return ""
}

// isLeader reports a lead or co-lead.
func isLeader(role int64) bool {
	return role == roleLead || role == roleCoLead
}

// canManage reports a lead, a co-lead or the human, who may invite, retire and change
// roles.
func canManage(role int64) bool {
	return isLeader(role) || role == roleHuman
}

// assignableRoles maps the role names that set_role accepts to their stored values.
var assignableRoles = map[string]int64{
	"worker":  roleWorker,
	"lead":    roleLead,
	"co-lead": roleCoLead,
}
