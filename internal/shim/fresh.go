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
	if worker.token != "" {
		joined := worker.joined.Copy()
		worker.mutex.Unlock()
		return joined, nil
	}
	worker.mutex.Unlock()
	joined, err := exchangeRest(call.ctx, worker.shim.core, &wire.Join{Workspace: worker.shim.workspace, Code: worker.code}, nil)
	if err != nil {
		return wire.Value{}, err
	}
	token, _ := joined.Delete("token")
	worker.mutex.Lock()
	if worker.token == "" {
		worker.token = token.StringOf()
		worker.joined = joined
	}
	result := worker.joined.Copy()
	worker.mutex.Unlock()
	return result, nil
}

// post posts a finding, blocker, or completion and forgets every quiet wait.
func (worker *freshWorker) post(call toolCall) (wire.Value, error) {
	token, err := worker.credential()
	if err != nil {
		return wire.Value{}, err
	}
	result, err := worker.shim.send(call, &wire.Post{Workspace: worker.shim.workspace, Token: token}, freshPostArguments)
	if err != nil {
		return wire.Value{}, err
	}
	worker.shim.state.clearWaits()
	return result, nil
}

// read reads after the cursor and optionally waits for activity or close guidance.
func (worker *freshWorker) read(call toolCall) (wire.Value, error) {
	token, err := worker.credential()
	if err != nil {
		return wire.Value{}, err
	}
	return worker.shim.readFor(call, &wire.Read{Workspace: worker.shim.workspace, Token: token}, freshReadArguments)
}

// status returns member activity, launch states and open tasks.
func (worker *freshWorker) status(call toolCall) (wire.Value, error) {
	token, err := worker.credential()
	if err != nil {
		return wire.Value{}, err
	}
	return worker.shim.statusFor(call, &wire.Status{Workspace: worker.shim.workspace, Token: token}, nil)
}
