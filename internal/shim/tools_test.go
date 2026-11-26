package shim

import (
	"slices"
	"testing"

	"github.com/witseko/agentcy/internal/wire"
	"github.com/witseko/agentcy/protocol"
)

// toolBinding is the request and argument bindings of one tool.
type toolBinding struct {
	request  wire.Request
	bindings []wire.Argument
}

// TestToolArgumentsFollowTheGoldenSignatures checks that each tool binds exactly the
// arguments of its golden input schema, in signature order, to fields of its request.
func TestToolArgumentsFollowTheGoldenSignatures(t *testing.T) {
	main := map[string]toolBinding{
		"create_conversation":       {&wire.Create{}, createArguments},
		"invite_participant":        {&wire.Invite{}, inviteArguments},
		"open_invite":               {&wire.OpenInvite{}, openInviteArguments},
		"retire_participant":        {&wire.Retire{}, nameArguments},
		"ping_participant":          {&wire.Ping{}, nameArguments},
		"set_participant_role":      {&wire.SetRole{}, setRoleArguments},
		"list_plugins":              {&wire.Plugins{}, tokenArguments},
		"join_conversation":         {&wire.Join{}, joinArguments},
		"report_usage":              {&wire.ReportUsage{}, usageArguments},
		"post_message":              {&wire.Post{}, postArguments},
		"read_messages":             {&wire.Read{}, readArguments},
		"launch_participant":        {&wire.Launch{}, launchArguments},
		"conversation_status":       {&wire.Status{}, tokenArguments},
		"release_proposals":         {&wire.Release{}, tokenArguments},
		"close_conversation":        {&wire.Close{}, closeArguments},
		"reopen_conversation":       {&wire.Reopen{}, reopenArguments},
		"list_closed_conversations": {&wire.ListClosed{}, listClosedArguments},
		"read_closed_conversation":  {&wire.ReadClosed{}, readClosedArguments},
	}
	fresh := map[string]toolBinding{
		"join_conversation":   {&wire.Join{}, nil},
		"post_message":        {&wire.Post{}, freshPostArguments},
		"read_messages":       {&wire.Read{}, freshReadArguments},
		"conversation_status": {&wire.Status{}, nil},
	}
	checkBindings(t, protocol.MCPTools, main)
	checkBindings(t, protocol.FreshWorkerTools, fresh)
}

// checkBindings compares the bindings with the argument names of a golden tool list.
func checkBindings(t *testing.T, golden []byte, tools map[string]toolBinding) {
	t.Helper()
	entries, err := goldenTools(golden)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != len(tools) {
		t.Errorf("golden list has %d tools, the test knows %d", len(entries), len(tools))
	}
	for _, entry := range entries {
		name := textField(entry, "name")
		tool, found := tools[name]
		if !found {
			t.Errorf("no binding for tool %s", name)
			continue
		}
		schema, _ := entry.Get("inputSchema")
		properties, _ := schema.Get("properties")
		var bound []string
		for _, binding := range tool.bindings {
			bound = append(bound, binding.Name)
		}
		if !slices.Equal(bound, properties.Keys()) {
			t.Errorf("%s binds %v, the golden signature is %v", name, bound, properties.Keys())
		}
		// ReadArguments panics on a binding to a field that the request does not have.
		_ = wire.ReadArguments(tool.request, name, wire.NewObject(), tool.bindings)
	}
}

// A forged requestState stops any tool before it reads its arguments or calls the core.
func TestForgedRequestStateStopsEveryToolBeforeTheCore(t *testing.T) {
	core := &fakeCore{}
	client := connectRaw(t, newTestServer(t, core), true, `{}`)
	_, rpcError := client.call("tools/call", `{"name":"post_message","arguments":{"token":"t","submission_id":"s","body":"b"},"requestState":"forged.state"}`)
	if rpcError == nil || rpcError.Message != "Invalid or expired requestState" {
		t.Errorf("forged state: %v", rpcError)
	}
	if actions := core.actions(); len(actions) != 0 {
		t.Errorf("the core received %v", actions)
	}
}
