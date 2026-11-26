package shim

import (
	"bufio"
	"context"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/witseko/agentcy/internal/wire"
)

func TestPingedWaitResetsSilentWaitsAndGuidesWithoutNoise(t *testing.T) {
	status := `{"open": true, "participant": "Worker", "members": {"Lead": {"role": "lead", "state": "replied"},
		"Worker": {"role": "worker", "state": "replied", "listening": true, "connected": true}}}`
	state := newListenState(true)
	page := finish(t, state, `{"messages": [], "pinged": true}`, status, 79, 0)
	if got := nextOf(page); !strings.Contains(got, "Ping received; your connection is active") || !strings.Contains(got, "wait_seconds=45") {
		t.Errorf("pinged worker next = %q", got)
	}
	if state.silentWaits["digest"] != 0 {
		t.Errorf("silentWaits = %d, want 0 after ping", state.silentWaits["digest"])
	}

	leadStatus := `{"open": true, "participant": "Lead", "members": {"Lead": {"role": "lead", "state": "replied"},
		"Worker": {"role": "worker", "state": "replied", "listening": true, "connected": true}}}`
	leadPage := finish(t, state, `{"messages": [], "pinged": true}`, leadStatus, 5, 0)
	if !strings.HasPrefix(nextOf(leadPage), "Ping received; your connection is active.") {
		t.Errorf("pinged lead next = %q", nextOf(leadPage))
	}
	team, _ := leadPage.Get("team")
	if !strings.Contains(team.Compact(), `"connected":true`) {
		t.Errorf("lead team snapshot lacks connected: %s", team.Compact())
	}
}

func TestPresenceTrackerStreamsSessionAndAnswersMCPHostPing(t *testing.T) {
	coreSide, shimSide := net.Pipe()
	defer coreSide.Close()

	coreMock := &fakeCore{answers: map[string]string{
		"join": `{"conversation":"c1","token":"member-token-1","brief":"Brief"}`,
		"ping": `{"participant":"Worker","status":"connected","listening":false,"connected":true,"woke":false,"ping_rtt_ms":1}`,
	}}
	server, err := NewServer(Config{
		Workspace: testWorkspace,
		Directory: "/tmp/checkout",
		Version:   "9.9.9",
		Core:      coreMock,
		DialSession: func(context.Context) (net.Conn, error) {
			return shimSide, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	serverSession, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer serverSession.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "test-host", Version: "1.0"}, nil)
	clientSession, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer clientSession.Close()

	// Calling join_conversation triggers presenceTracker.touch with the issued token.
	if _, err := clientSession.CallTool(ctx, &mcp.CallToolParams{
		Name:      "join_conversation",
		Arguments: map[string]any{"code": "join-code"},
	}); err != nil {
		t.Fatal(err)
	}

	reader := bufio.NewReader(coreSide)
	hello, err := reader.ReadString('\n')
	if err != nil || !strings.Contains(hello, `"action":"session"`) || !strings.Contains(hello, `"token":"member-token-1"`) {
		t.Fatalf("session hello = %q, err = %v", hello, err)
	}
	if err := wire.WriteLine(coreSide, []byte(`{"result":{"connected":true}}`)); err != nil {
		t.Fatal(err)
	}
	if err := wire.WriteLine(coreSide, []byte(`{"ping":true}`)); err != nil {
		t.Fatal(err)
	}
	pong, err := reader.ReadString('\n')
	if err != nil || strings.TrimSpace(pong) != `{"pong":true}` {
		t.Fatalf("pong = %q, err = %v", pong, err)
	}

	// Verify ping_participant forwards wire.Ping and launch_participant forwards relaunch=true.
	if _, err := clientSession.CallTool(ctx, &mcp.CallToolParams{
		Name:      "ping_participant",
		Arguments: map[string]any{"token": "lead-token", "name": "Worker"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := clientSession.CallTool(ctx, &mcp.CallToolParams{
		Name:      "launch_participant",
		Arguments: map[string]any{"token": "lead-token", "name": "Worker", "plugin": "glm", "relaunch": true},
	}); err != nil {
		t.Fatal(err)
	}
	launchReq, ok := coreMock.requests[len(coreMock.requests)-1].(*wire.Launch)
	if !ok || !launchReq.Relaunch {
		t.Fatalf("expected Launch with Relaunch=true, got %+v", coreMock.requests[len(coreMock.requests)-1])
	}

	// Closing the MCP client session closes shimSide so the core sees EOF immediately.
	_ = clientSession.Close()
	_ = coreSide.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := reader.ReadString('\n'); err == nil {
		t.Error("session stream stayed open after MCP client closed")
	}
}
