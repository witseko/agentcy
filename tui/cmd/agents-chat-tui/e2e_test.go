//go:build e2e

package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// This test starts a real core. Run it from the tui/ folder under the project's environment,
// so VIRTUAL_ENV names the interpreter that has agents_chat installed:
//
//	uv run --locked go test -tags e2e ./...

// repositoryRoot is the agents-chat clone, three levels above this package.
var repositoryRoot = filepath.Join("..", "..", "..")

// projectPython returns the interpreter of the environment that uv run activated.
func projectPython(t *testing.T) string {
	t.Helper()
	environment := os.Getenv("VIRTUAL_ENV")
	if environment == "" {
		t.Fatal("VIRTUAL_ENV is unset: run this test with `uv run --locked go test -tags e2e ./...`")
	}
	return filepath.Join(environment, "bin", "python")
}

// registerFixturePlugin writes a private plugins.json with the fixture plugin as "fixture".
func registerFixturePlugin(t *testing.T, home string, python string) {
	t.Helper()
	fixture, err := filepath.Abs(filepath.Join(repositoryRoot, "tests", "fixtures", "fake_run_plugin.py"))
	if err != nil {
		t.Fatal(err)
	}
	settings := map[string]any{"plugins": map[string]any{
		"fixture": map[string]any{
			"command": []string{python, fixture}, "model": "test/model", "timeout_seconds": 120,
		},
	}}
	content, err := json.Marshal(settings)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "plugins.json"), content, 0o600); err != nil {
		t.Fatal(err)
	}
}

// startCore runs `python -I -m agents_chat.mcp.core` with a short private AGENTS_CHAT_HOME
// and waits for its socket. The core stops when the test ends.
func startCore(t *testing.T) string {
	t.Helper()
	python := projectPython(t)
	home := shortHome(t)
	registerFixturePlugin(t, home, python)
	core := exec.Command(python, "-I", "-m", "agents_chat.mcp.core")
	core.Dir = home
	core.Env = []string{"AGENTS_CHAT_HOME=" + home, "PATH=/usr/bin:/bin"}
	logFile, err := os.Create(filepath.Join(home, "core.log"))
	if err != nil {
		t.Fatal(err)
	}
	core.Stdout, core.Stderr = logFile, logFile
	if err := core.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		core.Process.Kill()
		core.Wait()
		logFile.Close()
	})
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(home, socketFile)); err == nil {
			return home
		}
		if time.Now().After(deadline) {
			log, _ := os.ReadFile(filepath.Join(home, "core.log"))
			t.Fatalf("core did not start:\n%s", log)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestClientDrivesARealCore(t *testing.T) {
	home := startCore(t)
	workspace := filepath.Join(home, "workspace")
	if err := os.Mkdir(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	client := NewClient(home)

	plugins, err := client.Plugins()
	if err != nil || len(plugins.Plugins) != 1 || plugins.Plugins[0].Name != "fixture" {
		t.Fatalf("Plugins() = %+v, %v", plugins, err)
	}
	created, err := client.Create(CreateRequest{
		Directory: workspace, Brief: "Check the retry rule.", Lead: "Lead", Invitees: []string{"Worker"},
	})
	if err != nil || len(created.Invitations) != 2 || created.Invitations[0].Participant != "Lead" {
		t.Fatalf("Create() = %+v, %v", created, err)
	}
	conversation := created.Conversation
	queued, err := client.Launch(LaunchRequest{
		Directory: workspace, Conversation: conversation, Name: "Worker", Plugin: "fixture",
	})
	if err != nil || queued.AssignmentID == "" {
		t.Fatalf("Launch() = %+v, %v", queued, err)
	}
	posted, err := client.Post(PostRequest{
		Directory: workspace, Conversation: conversation, SubmissionID: submissionID(),
		Body: "Start with the tests.", Kind: "question", To: "Lead",
	})
	if err != nil || posted.MessageID == 0 {
		t.Fatalf("Post() = %+v, %v", posted, err)
	}

	view, err := client.Show(ShowRequest{Directory: workspace, Conversation: conversation})
	if err != nil {
		t.Fatalf("Show() error = %v", err)
	}
	last := view.Messages[len(view.Messages)-1]
	if last.ID != posted.MessageID || last.Label != humanName || last.Kind != "question" || last.To != "Lead" {
		t.Fatalf("last message = %+v", last)
	}
	if view.Status.Members["Worker"].LaunchState == "" || view.Status.Members[humanName].Role != "human" {
		t.Fatalf("members = %+v", view.Status.Members)
	}
	listed, err := client.List(ListRequest{Directory: workspace})
	if err != nil || len(listed.Conversations) != 1 || listed.Conversations[0].Conversation != conversation {
		t.Fatalf("List() = %+v, %v", listed, err)
	}

	closed, err := client.Close(CloseRequest{Directory: workspace, Conversation: conversation, Summary: "Done."})
	if err != nil || !closed.Closed {
		t.Fatalf("Close() = %+v, %v", closed, err)
	}
	view, err = client.Show(ShowRequest{Directory: workspace, Conversation: conversation, After: view.Cursor})
	if err != nil || view.Status.Open {
		t.Fatalf("Show() after close = %+v, %v", view.Status, err)
	}
}
