package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/witseko/agentcy/internal/paths"
)

// pythonUseStore opens the store of argv[2] in the AGENTCY_HOME of argv[1] with the
// Python code, creates a conversation through repository.apply when argv[3] is "create",
// and prints the briefs that a read of each conversation returns.
const pythonUseStore = `
import json, os, sys
os.environ["AGENTCY_HOME"] = sys.argv[1]
key = sys.argv[2]
from agentcy.conversations import models, repository
from agentcy.storage.database import database
engine = database(key)
def apply(connection, action, **fields):
    request = models.REQUEST.validate_python({"action": action, "workspace": key, **fields})
    return repository.apply(connection, request)
if sys.argv[3] == "create":
    with engine.begin() as connection:
        team = apply(connection, "create", brief="python crosscheck brief", initiator="Lead", invitees=["Worker"])
    with engine.begin() as connection:
        page = apply(connection, "read", token=team["token"])
    print(json.dumps({"brief": page["brief"]}))
with engine.connect() as connection:
    briefs = [row[0] for row in connection.exec_driver_sql("SELECT brief FROM conversation ORDER BY brief")]
print(json.dumps({"briefs": briefs}))
engine.dispose()
`

// runPythonStore runs pythonUseStore through uv, or skips the test when uv or the project
// environment is unavailable. It returns the last JSON line the script printed.
func runPythonStore(t *testing.T, home, key, mode string) map[string]any {
	t.Helper()
	pathValue, pathSet := os.LookupEnv("PATH")
	uv := paths.FindOnPath("uv", pathValue, pathSet)
	if uv == "" {
		t.Skip("uv is unavailable")
	}
	repository, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(uv, "run", "--locked", "--project", repository, "python", "-I", "-c", pythonUseStore, home, key, mode)
	command.Env = append(os.Environ(), paths.HomeVariable+"="+home)
	output, err := command.Output()
	if err != nil {
		if exit, ok := err.(*exec.ExitError); ok {
			t.Fatalf("Python failed: %v\n%s", err, exit.Stderr)
		}
		t.Skipf("Python unavailable: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	var result map[string]any
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &result); err != nil {
		t.Fatalf("parse %q: %v", output, err)
	}
	return result
}

// goBriefs reads the conversation briefs of a store through the Go read pool.
func goBriefs(t *testing.T, store *Store) []any {
	t.Helper()
	briefs := []any{}
	err := store.Read(context.Background(), func(tx *sql.Tx) error {
		rows, err := tx.Query("SELECT brief FROM conversation ORDER BY brief")
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var brief string
			if err := rows.Scan(&brief); err != nil {
				return err
			}
			briefs = append(briefs, brief)
		}
		return rows.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	return briefs
}

// TestPythonUsesGoCreatedStore creates a store in Go, then lets the Python code open it,
// create a conversation and read it, and reads the Python rows back in Go.
func TestPythonUsesGoCreatedStore(t *testing.T) {
	ctx := context.Background()
	home := newDataDir(t)
	store, err := Open(ctx, home, testKey)
	if err != nil {
		t.Fatal(err)
	}
	err = store.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "INSERT INTO conversation (id, brief, open, archived) VALUES ('go-room', 'go brief', 1, 0)")
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	result := runPythonStore(t, home, testKey, "create")
	if got, _ := json.Marshal(result["briefs"]); string(got) != `["go brief","python crosscheck brief"]` {
		t.Errorf("Python sees briefs %s", got)
	}
	if got, _ := json.Marshal(goBriefs(t, store)); string(got) != `["go brief","python crosscheck brief"]` {
		t.Errorf("Go sees briefs %s", got)
	}
}

// TestGoOpensPythonCreatedStore lets the Python code create a store and a conversation,
// then opens it in Go, reads the row and searches the full-text index.
func TestGoOpensPythonCreatedStore(t *testing.T) {
	ctx := context.Background()
	home := newDataDir(t)
	runPythonStore(t, home, testKey, "create")
	store, err := Open(ctx, home, testKey)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := json.Marshal(goBriefs(t, store)); string(got) != `["python crosscheck brief"]` {
		t.Errorf("Go sees briefs %s", got)
	}
	err = store.Read(ctx, func(tx *sql.Tx) error {
		var count int
		row := tx.QueryRowContext(ctx, "SELECT count(*) FROM conversation_search WHERE conversation_search MATCH ?", "crosscheck")
		if err := row.Scan(&count); err != nil {
			return err
		}
		if count != 1 {
			t.Errorf("search hits = %d, want 1", count)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
