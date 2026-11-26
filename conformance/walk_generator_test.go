//go:build conformance

package conformance

import (
	"fmt"
	"math/rand"
	"sort"
)

// secret is a value that each daemon generates differently, such as a token, a join
// code or a conversation ID: index 0 is the reference daemon's, index 1 the tested one's.
type secret [2]string

// substitute returns a request for one daemon, with every secret replaced by that
// daemon's value.
func substitute(request fields, daemon int) fields {
	result := fields{}
	for key, value := range request {
		if hidden, isSecret := value.(*secret); isSecret {
			result[key] = hidden[daemon]
			continue
		}
		result[key] = value
	}
	return result
}

// secretAt reads the same path from both replies; nil when the reference has nothing
// there.
func secretAt(replies [2]walkReply, path ...any) *secret {
	reference := replies[0].text(path...)
	if reference == "" {
		return nil
	}
	return &secret{reference, replies[1].text(path...)}
}

// walkAction is one generated request with the state update it causes.
type walkAction struct {
	label   string
	request fields
	observe func(replies [2]walkReply)
}

// walkMember is a slot that the walk knows about.
type walkMember struct {
	name  string
	token *secret
	code  *secret
}

// walkRoom is a conversation that the walk created.
type walkRoom struct {
	conversation *secret
	members      []*walkMember
	openCode     *secret
	messages     []int64
	tasks        []int64
	handoffs     []int64
	cursor       map[string]int64
	// closed is the last close or reopen that succeeded.
	closed bool
	// taskOwners and handoffTargets remember who should act next on a task or handoff,
	// so task flows succeed often enough to reach the deeper rules.
	taskOwners     map[int64]string
	handoffTargets map[int64]string
	handoffTasks   map[int64]int64
}

// walkGenerator produces a seeded sequence of requests and tracks what they create.
type walkGenerator struct {
	random      *rand.Rand
	workspace   string
	directory   string
	operatorKey *secret
	rooms       []*walkRoom
	submissions int
	// lastPost repeats a recent post, so retries and deduplication are exercised.
	lastPost fields
}

// newWalkGenerator returns a generator for one seed and workspace.
func newWalkGenerator(seed int64, workspace, directory string, operatorKeys [2]string) *walkGenerator {
	key := secret(operatorKeys)
	return &walkGenerator{random: rand.New(rand.NewSource(seed)), workspace: workspace, directory: directory, operatorKey: &key}
}

// build returns the request for one daemon.
func (generator *walkGenerator) build(request fields, daemon int) fields {
	return substitute(request, daemon)
}

// The pools that requests draw from. Some names are reserved or look alike on purpose.
var (
	walkNames = []string{"Worker", "Analyst", "Reviewer", "worker", "Ann", "Bob", "Zoë", "Ärger", "W 2", "Lead", "_x", "Ωmega", "a"}
	// walkReservedNames are the human's names and look-alikes, drawn now and then.
	walkReservedNames = []string{"Human", "Hυman", "You", "hum an"}
	walkBodies        = []string{
		"Please check the retry rule.", "Done with the parser.", "Blocked on the fixture.", "Resuming now.",
		"Proposal: cache the key.", "Look at  file.go:12\n\nsecond line", "café résumé naïve", "Ünïcödé ⅷ ½ test_case",
		"short", "Another note about the brief and the roster.",
	}
	walkKinds   = []string{"note", "note", "question", "finding", "blocker", "resume", "done", "task", "task", "handoff", "accept", "decline", "proposal"}
	walkModels  = []string{"model-a", "model-b", "MODEL-A", "other"}
	walkQueries = []string{"retry", "parser rule", "café", "ⅷ", "test_case", "!!!", "brief", "Proposal cache"}
)

// name returns a member name, rarely a reserved one.
func (generator *walkGenerator) name() string {
	if generator.chance(4) {
		return pick(generator.random, walkReservedNames)
	}
	return pick(generator.random, walkNames)
}

// pick returns a random element.
func pick[T any](random *rand.Rand, values []T) T {
	return values[random.Intn(len(values))]
}

// chance is true with probability percent / 100.
func (generator *walkGenerator) chance(percent int) bool {
	return generator.random.Intn(100) < percent
}

// next returns the next action. Without rooms it creates one.
func (generator *walkGenerator) next() walkAction {
	if len(generator.rooms) == 0 || generator.chance(4) {
		if generator.chance(50) {
			return generator.create()
		}
		return generator.operatorCreate()
	}
	room := pick(generator.random, generator.rooms)
	if room.closed && generator.chance(30) {
		return generator.reopen(room)
	}
	roll := generator.random.Intn(100)
	switch {
	case roll < 10:
		return generator.join(room)
	case roll < 42:
		return generator.post(room)
	case roll < 50:
		return generator.read(room)
	case roll < 55:
		return generator.status(room)
	case roll < 62:
		return generator.manage(room)
	case roll < 66:
		return generator.closeOrReopen(room)
	case roll < 69:
		return generator.reportUsage(room)
	case roll < 72:
		return generator.history(room)
	}
	return generator.operatorAction(room)
}

// member returns a random known member of a room, or nil.
func (generator *walkGenerator) member(room *walkRoom) *walkMember {
	if len(room.members) == 0 {
		return nil
	}
	return pick(generator.random, room.members)
}

// joinedMember returns a random member that holds a token, or nil.
func (generator *walkGenerator) joinedMember(room *walkRoom) *walkMember {
	var joined []*walkMember
	for _, member := range room.members {
		if member.token != nil {
			joined = append(joined, member)
		}
	}
	if len(joined) == 0 {
		return nil
	}
	return pick(generator.random, joined)
}

// findMember returns the member with exactly this name, or nil.
func (room *walkRoom) findMember(name string) *walkMember {
	for _, member := range room.members {
		if member.name == name {
			return member
		}
	}
	return nil
}

// memberRequest builds a member request with the workspace, the action and a token.
func (generator *walkGenerator) memberRequest(action string, token *secret) fields {
	request := fields{"action": action, "workspace": generator.workspace}
	if token != nil {
		request["token"] = token
	} else {
		request["token"] = "not-a-token"
	}
	return request
}

// operatorRequest builds an operator request for the walk's checkout.
func (generator *walkGenerator) operatorRequest(action string, room *walkRoom) fields {
	request := fields{"version": 1, "key": generator.operatorKey, "action": action, "directory": generator.directory}
	if room != nil {
		request["conversation"] = room.conversation
	}
	return request
}

// invitees returns one to three invitees, sometimes as invitation objects.
func (generator *walkGenerator) invitees() []any {
	count := 1 + generator.random.Intn(3)
	invitees := []any{}
	for range count {
		name := generator.name()
		if generator.chance(30) {
			invitation := fields{"name": name}
			if generator.chance(40) {
				invitation["role"] = "co-lead"
			}
			if generator.chance(40) {
				invitation["model"] = pick(generator.random, walkModels)
			}
			if generator.chance(20) {
				invitation["token_budget"] = 1000 + generator.random.Intn(5000)
			}
			invitees = append(invitees, invitation)
			continue
		}
		invitees = append(invitees, name)
	}
	return invitees
}

// roundSettings sometimes makes a new conversation sealed or open to anyone.
func (generator *walkGenerator) roundSettings(request fields) {
	if generator.chance(25) {
		request["open_invite"] = true
	}
	if generator.chance(20) {
		request["sealed"] = true
		if generator.chance(50) {
			// Long deadlines, so no round is released by time during a walk.
			request["deadline_minutes"] = 30 + generator.random.Intn(31)
		}
	}
}

// newRoomFrom records a conversation that a create reply describes.
func (generator *walkGenerator) newRoomFrom(replies [2]walkReply) *walkRoom {
	conversation := secretAt(replies, "conversation")
	if conversation == nil {
		return nil
	}
	room := &walkRoom{
		conversation: conversation, openCode: secretAt(replies, "open_invite", "code"), cursor: map[string]int64{},
		taskOwners: map[int64]string{}, handoffTargets: map[int64]string{}, handoffTasks: map[int64]int64{},
	}
	generator.rooms = append(generator.rooms, room)
	return room
}

// create is a member create.
func (generator *walkGenerator) create() walkAction {
	initiator := generator.name()
	request := fields{"action": "create", "workspace": generator.workspace, "brief": pick(generator.random, walkBodies),
		"initiator": initiator, "invitees": generator.invitees()}
	if generator.chance(30) {
		request["initiator_model"] = pick(generator.random, walkModels)
	}
	generator.roundSettings(request)
	return walkAction{label: "create", request: request, observe: func(replies [2]walkReply) {
		room := generator.newRoomFrom(replies)
		if room == nil {
			return
		}
		room.members = append(room.members, &walkMember{name: initiator, token: secretAt(replies, "token")})
		invitations, _ := replies[0].at("invitations").(map[string]any)
		for name := range invitations {
			room.members = append(room.members, &walkMember{name: name, code: secretAt(replies, "invitations", name)})
		}
	}}
}

// operatorCreate is a create by the human.
func (generator *walkGenerator) operatorCreate() walkAction {
	request := generator.operatorRequest("operator_create", nil)
	request["brief"] = pick(generator.random, walkBodies)
	request["lead"] = generator.name()
	request["invitees"] = generator.invitees()
	generator.roundSettings(request)
	return walkAction{label: "operator_create", request: request, observe: func(replies [2]walkReply) {
		room := generator.newRoomFrom(replies)
		if room == nil {
			return
		}
		invitations, _ := replies[0].at("invitations").([]any)
		for index := range invitations {
			room.members = append(room.members, &walkMember{
				name: replies[0].text("invitations", index, "participant"),
				code: secretAt(replies, "invitations", index, "code"),
			})
		}
	}}
}

// join redeems an unused slot code most of the time, else the open code, a used code
// or a bad code.
func (generator *walkGenerator) join(room *walkRoom) walkAction {
	request := fields{"action": "join", "workspace": generator.workspace}
	var unused []*walkMember
	for _, member := range room.members {
		if member.code != nil && member.token == nil {
			unused = append(unused, member)
		}
	}
	roll := generator.random.Intn(10)
	member := generator.member(room)
	switch {
	case roll < 6 && len(unused) > 0:
		request["code"] = pick(generator.random, unused).code
	case roll < 8 && room.openCode != nil:
		request["code"] = room.openCode
		if generator.chance(50) {
			request["name"] = generator.name()
		}
	case roll < 9 && member != nil && member.code != nil:
		request["code"] = member.code
	case generator.chance(50):
		request["code"] = "AAAAAAAAAAAAAAAAAAAAAA.0123456789ab"
	default:
		request["code"] = "plain-unknown-code"
	}
	if generator.chance(30) {
		request["model"] = pick(generator.random, walkModels)
	}
	return walkAction{label: "join", request: request, observe: func(replies [2]walkReply) {
		token := secretAt(replies, "token")
		if token == nil {
			return
		}
		name := replies[0].text("participant")
		joined := room.findMember(name)
		if joined == nil {
			joined = &walkMember{name: name}
			room.members = append(room.members, joined)
		}
		joined.token = token
	}}
}

// post sends a message of a random kind, sometimes repeating the last post.
func (generator *walkGenerator) post(room *walkRoom) walkAction {
	member := generator.joinedMember(room)
	if generator.lastPost != nil && generator.chance(8) {
		request := fields{}
		for key, value := range generator.lastPost {
			request[key] = value
		}
		if generator.chance(50) {
			generator.submissions++
			request["submission_id"] = fmt.Sprintf("s%d", generator.submissions)
		} else if generator.chance(30) {
			request["body"] = pick(generator.random, walkBodies)
		}
		return generator.postAction(room, request)
	}
	if generator.chance(40) {
		if request := generator.taskFlowPost(room); request != nil {
			generator.lastPost = request
			return generator.postAction(room, request)
		}
	}
	var token *secret
	if member != nil {
		token = member.token
	}
	request := generator.memberRequest("post", token)
	generator.fillSubmission(room, request)
	generator.lastPost = request
	return generator.postAction(room, request)
}

// taskFlowPost builds a post that the task rules should accept: a handoff by the
// owner, a response by the handoff's receiver, or a blocker, resume or done by the
// owner. It returns nil when the room has no task to act on.
func (generator *walkGenerator) taskFlowPost(room *walkRoom) fields {
	if len(room.handoffTargets) > 0 && generator.chance(40) {
		handoff := pick(generator.random, sortedKeys(room.handoffTargets))
		receiver := room.findMember(room.handoffTargets[handoff])
		if receiver == nil || receiver.token == nil {
			return nil
		}
		request := generator.memberRequest("post", receiver.token)
		generator.submissions++
		request["submission_id"] = fmt.Sprintf("s%d", generator.submissions)
		request["body"] = pick(generator.random, walkBodies)
		request["kind"] = pick(generator.random, []string{"accept", "decline"})
		request["reply_to"] = handoff
		return request
	}
	if len(room.taskOwners) == 0 {
		return nil
	}
	taskID := pick(generator.random, sortedKeys(room.taskOwners))
	owner := room.findMember(room.taskOwners[taskID])
	if owner == nil || owner.token == nil {
		return nil
	}
	request := generator.memberRequest("post", owner.token)
	generator.submissions++
	request["submission_id"] = fmt.Sprintf("s%d", generator.submissions)
	request["body"] = pick(generator.random, walkBodies)
	kind := pick(generator.random, []string{"handoff", "handoff", "blocker", "resume", "done"})
	request["kind"] = kind
	request["reply_to"] = taskID
	if kind == "handoff" {
		for range 5 {
			if receiver := generator.joinedMember(room); receiver != nil && receiver != owner {
				request["to"] = receiver.name
				break
			}
		}
	}
	return request
}

// sortedKeys returns the keys of a map in increasing order, so the walk stays
// reproducible.
func sortedKeys(values map[int64]string) []int64 {
	keys := make([]int64, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(left, right int) bool { return keys[left] < keys[right] })
	return keys
}

// fillSubmission adds a submission ID, body, kind, addressee, reply target and timeline.
func (generator *walkGenerator) fillSubmission(room *walkRoom, request fields) {
	generator.submissions++
	request["submission_id"] = fmt.Sprintf("s%d", generator.submissions)
	request["body"] = pick(generator.random, walkBodies)
	kind := pick(generator.random, walkKinds)
	request["kind"] = kind
	if target := generator.addressee(room, kind); target != "" {
		request["to"] = target
	}
	if reply := generator.replyTarget(room, kind); reply > 0 {
		request["reply_to"] = reply
	}
	if (kind == "task" && !generator.chance(10)) || (kind != "task" && generator.chance(3)) {
		request["due_minutes"] = 1 + generator.random.Intn(120)
	}
}

// addressee picks a recipient name that fits the kind most of the time.
func (generator *walkGenerator) addressee(room *walkRoom, kind string) string {
	wantsOne := kind == "task" || kind == "handoff"
	if !generator.chance(map[bool]int{true: 90, false: 25}[wantsOne]) {
		return ""
	}
	if generator.chance(10) {
		return pick(generator.random, []string{"Human", "Ghost", "human"})
	}
	member := generator.member(room)
	if joined := generator.joinedMember(room); wantsOne && joined != nil && generator.chance(80) {
		member = joined
	}
	if member != nil {
		return member.name
	}
	return ""
}

// replyTarget picks a reply target that fits the kind most of the time.
func (generator *walkGenerator) replyTarget(room *walkRoom, kind string) int64 {
	switch {
	case (kind == "accept" || kind == "decline") && len(room.handoffs) > 0 && generator.chance(85):
		return pick(generator.random, room.handoffs)
	case (kind == "done" || kind == "blocker" || kind == "resume" || kind == "handoff") && len(room.tasks) > 0 && generator.chance(85):
		return pick(generator.random, room.tasks)
	case len(room.messages) > 0 && generator.chance(20):
		return pick(generator.random, room.messages)
	case generator.chance(3):
		return 99999
	}
	return 0
}

// postAction records the IDs that a post creates.
func (generator *walkGenerator) postAction(room *walkRoom, request fields) walkAction {
	label := fmt.Sprintf("%s %v", request["action"], request["kind"])
	return walkAction{label: label, request: request, observe: func(replies [2]walkReply) {
		number := replies[0].number("message_id")
		if number == 0 {
			return
		}
		room.messages = append(room.messages, number)
		to, _ := request["to"].(string)
		reply, _ := request["reply_to"].(int64)
		switch request["kind"] {
		case "task":
			room.tasks = append(room.tasks, number)
			room.taskOwners[number] = to
		case "handoff":
			room.handoffs = append(room.handoffs, number)
			room.handoffTargets[number] = to
			room.handoffTasks[number] = reply
		case "accept":
			room.taskOwners[room.handoffTasks[reply]] = room.handoffTargets[reply]
			delete(room.handoffTargets, reply)
		case "decline":
			delete(room.handoffTargets, reply)
		case "done":
			delete(room.taskOwners, reply)
		}
	}}
}

// read reads from zero, from the member's last cursor, or from a random cursor.
func (generator *walkGenerator) read(room *walkRoom) walkAction {
	member := generator.joinedMember(room)
	var token *secret
	name := ""
	if member != nil {
		token, name = member.token, member.name
	}
	request := generator.memberRequest("read", token)
	switch roll := generator.random.Intn(3); roll {
	case 0:
		request["after"] = room.cursor[name]
	case 1:
		if len(room.messages) > 0 {
			request["after"] = pick(generator.random, room.messages)
		}
	}
	return walkAction{label: "read", request: request, observe: func(replies [2]walkReply) {
		if replies[0].at("cursor") != nil {
			room.cursor[name] = replies[0].number("cursor")
		}
	}}
}

// status reads the team status, sometimes with a token of no member.
func (generator *walkGenerator) status(room *walkRoom) walkAction {
	member := generator.joinedMember(room)
	var token *secret
	if member != nil && !generator.chance(5) {
		token = member.token
	}
	return walkAction{label: "status", request: generator.memberRequest("status", token)}
}

// manage is a lead action: invite, retire, set_role, open_invite, release or plugins.
func (generator *walkGenerator) manage(room *walkRoom) walkAction {
	member := generator.joinedMember(room)
	var token *secret
	if member != nil {
		token = member.token
	}
	target := generator.name()
	if known := generator.member(room); known != nil && generator.chance(70) {
		target = known.name
	}
	switch generator.random.Intn(6) {
	case 0:
		request := generator.memberRequest("invite", token)
		request["name"] = target
		if generator.chance(40) {
			request["role"] = pick(generator.random, []string{"worker", "co-lead"})
		}
		if generator.chance(30) {
			request["reinvite"] = true
		}
		return generator.inviteAction(room, request, target)
	case 1:
		request := generator.memberRequest("retire", token)
		request["name"] = target
		return walkAction{label: "retire", request: request}
	case 2:
		request := generator.memberRequest("set_role", token)
		request["name"] = target
		request["role"] = pick(generator.random, []string{"worker", "lead", "co-lead"})
		return walkAction{label: "set_role", request: request}
	case 3:
		request := generator.memberRequest("open_invite", token)
		request["enabled"] = generator.chance(70)
		return generator.openInviteAction(room, request)
	case 4:
		return walkAction{label: "release", request: generator.memberRequest("release", token)}
	}
	return walkAction{label: "plugins", request: generator.memberRequest("plugins", token)}
}

// inviteAction records the code that an invitation returns.
func (generator *walkGenerator) inviteAction(room *walkRoom, request fields, name string) walkAction {
	return walkAction{label: fmt.Sprintf("%v", request["action"]), request: request, observe: func(replies [2]walkReply) {
		code := secretAt(replies, "code")
		if code == nil {
			return
		}
		label := replies[0].text("participant")
		invited := room.findMember(label)
		if invited == nil {
			invited = &walkMember{name: label}
			room.members = append(room.members, invited)
		}
		invited.code = code
	}}
}

// openInviteAction records the open code that an open-invite change returns.
func (generator *walkGenerator) openInviteAction(room *walkRoom, request fields) walkAction {
	return walkAction{label: fmt.Sprintf("%v", request["action"]), request: request, observe: func(replies [2]walkReply) {
		if code := secretAt(replies, "code"); code != nil {
			room.openCode = code
		}
	}}
}

// closeOrReopen closes as a member most of the time, else reopens.
func (generator *walkGenerator) closeOrReopen(room *walkRoom) walkAction {
	if !generator.chance(60) {
		return generator.reopen(room)
	}
	member := generator.joinedMember(room)
	var token *secret
	if member != nil {
		token = member.token
	}
	request := generator.memberRequest("close", token)
	if generator.chance(50) {
		request["summary"] = pick(generator.random, walkBodies)
	}
	return generator.closingAction(room, "close", request)
}

// reopen reopens as a member or as the human.
func (generator *walkGenerator) reopen(room *walkRoom) walkAction {
	member := generator.joinedMember(room)
	request := generator.operatorRequest("operator_reopen", room)
	if member != nil && generator.chance(50) {
		request = generator.memberRequest("reopen", member.token)
	}
	if generator.chance(40) {
		request["note"] = pick(generator.random, walkBodies)
	}
	return generator.closingAction(room, fmt.Sprintf("%v", request["action"]), request)
}

// closingAction records whether a close or reopen succeeded.
func (generator *walkGenerator) closingAction(room *walkRoom, label string, request fields) walkAction {
	return walkAction{label: label, request: request, observe: func(replies [2]walkReply) {
		if replies[0].at("closed") == true {
			room.closed = true
		}
		if replies[0].at("reopened") == true {
			room.closed = false
		}
	}}
}

// reportUsage reports exact usage, sometimes with a model.
func (generator *walkGenerator) reportUsage(room *walkRoom) walkAction {
	member := generator.joinedMember(room)
	var token *secret
	if member != nil {
		token = member.token
	}
	request := generator.memberRequest("report_usage", token)
	request["input_tokens"] = generator.random.Intn(8000)
	request["output_tokens"] = generator.random.Intn(3000)
	if generator.chance(50) {
		request["model"] = pick(generator.random, walkModels)
	}
	return walkAction{label: "report_usage", request: request}
}

// history lists or reads closed conversations without a token.
func (generator *walkGenerator) history(room *walkRoom) walkAction {
	if generator.chance(40) {
		request := fields{"action": "list_closed", "workspace": generator.workspace}
		if generator.chance(50) {
			request["archived"] = generator.chance(50)
		}
		return walkAction{label: "list_closed", request: request}
	}
	request := fields{"action": "read_closed", "workspace": generator.workspace, "conversation": room.conversation}
	if generator.chance(40) && len(room.messages) > 0 {
		request["after"] = pick(generator.random, room.messages)
	}
	return walkAction{label: "read_closed", request: request}
}

// operatorAction is one operator request about a room.
func (generator *walkGenerator) operatorAction(room *walkRoom) walkAction {
	target := generator.name()
	if known := generator.member(room); known != nil && generator.chance(70) {
		target = known.name
	}
	switch generator.random.Intn(14) {
	case 0, 1:
		request := generator.operatorRequest("operator_post", room)
		generator.fillSubmission(room, request)
		request["kind"] = pick(generator.random, []string{"note", "question"})
		return generator.postAction(room, request)
	case 2:
		request := generator.operatorRequest("operator_invite", room)
		request["name"] = target
		if generator.chance(40) {
			request["role"] = pick(generator.random, []string{"worker", "co-lead"})
		}
		if generator.chance(30) {
			request["reinvite"] = true
		}
		return generator.inviteAction(room, request, target)
	case 3:
		request := generator.operatorRequest("operator_open_invite", room)
		request["enabled"] = generator.chance(70)
		return generator.openInviteAction(room, request)
	case 4:
		request := generator.operatorRequest("operator_retire", room)
		request["name"] = target
		return walkAction{label: "operator_retire", request: request}
	case 5:
		request := generator.operatorRequest("operator_set_role", room)
		request["name"] = target
		request["role"] = pick(generator.random, []string{"worker", "lead", "co-lead"})
		return walkAction{label: "operator_set_role", request: request}
	case 6:
		request := generator.operatorRequest("operator_close", room)
		if generator.chance(40) {
			request["summary"] = pick(generator.random, walkBodies)
		}
		if generator.chance(40) {
			request["deprecate_tasks"] = true
		}
		return generator.closingAction(room, "operator_close", request)
	case 7:
		return generator.reopen(room)
	case 8:
		request := generator.operatorRequest("operator_archive", room)
		request["archived"] = generator.chance(60)
		if generator.chance(30) {
			request["deprecate_tasks"] = true
		}
		return walkAction{label: "operator_archive", request: request}
	case 9:
		request := generator.operatorRequest("operator_rename", room)
		if generator.chance(70) {
			request["title"] = pick(generator.random, []string{"Retry work", "Ünïcödé title", "A title that is long enough to be cut in the list view because it goes on"})
		}
		return walkAction{label: "operator_rename", request: request}
	case 10:
		request := generator.operatorRequest("operator_set_member_budget", room)
		request["name"] = target
		if generator.chance(60) {
			request["model"] = pick(generator.random, walkModels)
		}
		if generator.chance(60) {
			request["token_budget"] = 500 + generator.random.Intn(9000)
		}
		return walkAction{label: "operator_set_member_budget", request: request}
	case 11:
		return generator.operatorShow(room)
	case 12:
		return walkAction{label: "operator_tasks", request: generator.operatorRequest("operator_tasks", room)}
	}
	if generator.chance(50) {
		request := generator.operatorRequest("operator_list", nil)
		request["archived"] = generator.chance(30)
		return walkAction{label: "operator_list", request: request}
	}
	request := generator.operatorRequest("operator_search", nil)
	request["query"] = pick(generator.random, walkQueries)
	return walkAction{label: "operator_search", request: request}
}

// operatorShow shows a room with a random page selection.
func (generator *walkGenerator) operatorShow(room *walkRoom) walkAction {
	request := generator.operatorRequest("operator_show", room)
	switch generator.random.Intn(4) {
	case 0:
		request["tail"] = true
	case 1:
		if len(room.messages) > 0 {
			request["before"] = pick(generator.random, room.messages)
		}
	case 2:
		if len(room.messages) > 0 {
			request["after"] = pick(generator.random, room.messages)
		}
	}
	return walkAction{label: "operator_show", request: request}
}
