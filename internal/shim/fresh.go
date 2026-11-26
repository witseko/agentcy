package shim

import (
	"errors"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/witseko/agentcy/internal/wire"
	"github.com/witseko/agentcy/protocol"
)

// FreshJoinCodeVariable carries the fresh worker's private join code. The caller removes
// it from the environment before anything else runs.
const FreshJoinCodeVariable = "AGENTCY_FRESH_JOIN_CODE"

// errNotJoined is the tool error of the fresh worker before join_conversation.
var errNotJoined = errors.New("Join your assigned conversation first")

// freshWorker binds an OpenCode worker to its assigned slot without exposing the join
// code or the member token to the model. Port of src/agentcy/mcp/fresh_server.py.
type freshWorker struct {
	shim   *shimServer
	code   string
	mutex  sync.Mutex
	token  string
	joined wire.Value
}

// NewFreshWorkerServer builds the MCP server that exposes only the joined worker's join,
// post, read and status operations.
func NewFreshWorkerServer(workspace, code string, core Exchanger) (*mcp.Server, error) {
	signer, err := newRequestStateSigner(time.Now)
	if err != nil {
		return nil, err
	}
	shim := &shimServer{workspace: workspace, core: core, state: newListenState(false), signer: signer}
	worker := &freshWorker{shim: shim, code: code}
	shim.tools = map[string]toolFunction{
		"join_conversation":   worker.join,
		"post_message":        worker.post,
		"read_messages":       worker.read,
		"conversation_status": worker.status,
	}
	return shim.build(&mcp.Implementation{Name: "agentcy-fresh-worker"}, protocol.FreshWorkerTools, protocol.FreshWorkerToolsLegacy)
}

// credential returns the member token, or an error before the join.
func (worker *freshWorker) credential() (string, error) {
	worker.mutex.Lock()
	defer worker.mutex.Unlock()
	if worker.token == "" {
		return "", errNotJoined
	}
	return worker.token, nil
}

// join redeems the assigned code once and returns the brief and roster without the
// token. Later calls return the first answer.
func (worker *freshWorker) join(call toolCall) (wire.Value, error) {
	worker.mutex.Lock()
	defer worker.mutex.Unlock()
	if worker.token == "" {
		joined, err := exchangeValid(worker.shim.core, &wire.Join{Workspace: worker.shim.workspace, Code: worker.code})
		if err != nil {
			return wire.Value{}, err
		}
		token, _ := joined.Delete("token")
		worker.token = token.StringOf()
		worker.joined = joined
	}
	return worker.joined.Copy(), nil
}

// post posts a finding, blocker, or completion and forgets every quiet wait.
func (worker *freshWorker) post(call toolCall) (wire.Value, error) {
	arguments := call.arguments
	request := &wire.Post{Workspace: worker.shim.workspace}
	request.SubmissionID = arguments.key("submission_id")
	request.Body = arguments.body("body")
	request.Kind = arguments.literal("kind", "note", wire.PostKinds)
	request.To = arguments.optionalLabel("to")
	request.ReplyTo = arguments.optionalStrictInt("reply_to", wire.MessageRange)
	if err := arguments.err("post_message"); err != nil {
		return wire.Value{}, err
	}
	token, err := worker.credential()
	if err != nil {
		return wire.Value{}, err
	}
	request.Token = token
	result, err := exchangeValid(worker.shim.core, request)
	if err != nil {
		return wire.Value{}, err
	}
	worker.shim.state.clearWaits()
	return result, nil
}

// read reads after the cursor and optionally waits for activity or close guidance.
func (worker *freshWorker) read(call toolCall) (wire.Value, error) {
	arguments := call.arguments
	after := arguments.cursor("after")
	waitSeconds := arguments.strictInt("wait_seconds", 0, wire.WaitRange)
	if err := arguments.err("read_messages"); err != nil {
		return wire.Value{}, err
	}
	token, err := worker.credential()
	if err != nil {
		return wire.Value{}, err
	}
	return readMember(worker.shim.core, worker.shim.state, worker.shim.workspace, token, after, waitSeconds)
}

// status returns member activity, launch states and open tasks.
func (worker *freshWorker) status(call toolCall) (wire.Value, error) {
	token, err := worker.credential()
	if err != nil {
		return wire.Value{}, err
	}
	status, err := exchangeValid(worker.shim.core, &wire.Status{Workspace: worker.shim.workspace, Token: token})
	if err != nil {
		return wire.Value{}, err
	}
	status.Delete("participant")
	return status, nil
}
