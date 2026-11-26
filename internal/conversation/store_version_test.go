package conversation

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"log"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/witseko/agentcy/internal/core"
	"github.com/witseko/agentcy/internal/wire"
)

// writeStoreAtVersion creates a workspace store file whose user_version is version, as a
// store last opened by an older agentcy has.
func writeStoreAtVersion(t *testing.T, dataDir, workspace string, version int) {
	t.Helper()
	database, err := sql.Open("sqlite", filepath.Join(dataDir, workspace+".sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if _, err := database.Exec("CREATE TABLE conversation (id TEXT PRIMARY KEY)"); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(fmt.Sprintf("PRAGMA user_version=%d", version)); err != nil {
		t.Fatal(err)
	}
}

func TestOldStoreIsRefusedWithThePythonMessageAndLoggedWithTheFix(t *testing.T) {
	dataDir := t.TempDir()
	workspace := strings.Repeat("cd", 32)
	writeStoreAtVersion(t, dataDir, workspace, 14)
	var logged bytes.Buffer
	handler := NewHandler(newTestShared(t, dataDir, log.New(&logged, "", 0)), nil, nil)

	_, err := handler.Member(context.Background(), &wire.ListClosed{Workspace: workspace}, time.Unix(startUnix, 0))

	rejection, isRejection := err.(*core.Rejection)
	if !isRejection || rejection.Message != "Unsupported bridge database version" {
		t.Fatalf("error = %v, want the rejection %q", err, "Unsupported bridge database version")
	}
	line := logged.String()
	for _, want := range []string{workspace + ".sqlite3", "schema version 15", "make stop-core", "make migrate-stores"} {
		if !strings.Contains(line, want) {
			t.Errorf("log line %q lacks %q", line, want)
		}
	}
}

func TestIdleCheckRefusesAnOldStoreWithThePythonMessage(t *testing.T) {
	dataDir := t.TempDir()
	writeStoreAtVersion(t, dataDir, strings.Repeat("ef", 32), 14)
	var logged bytes.Buffer
	handler := NewHandler(newTestShared(t, dataDir, log.New(&logged, "", 0)), nil, nil)

	_, err := handler.HasActiveWork(context.Background(), time.Unix(startUnix, 0))

	if err == nil || err.Error() != "Unsupported bridge database version" {
		t.Fatalf("error = %v, want %q", err, "Unsupported bridge database version")
	}
	if !strings.Contains(logged.String(), "make migrate-stores") {
		t.Errorf("log %q does not tell the user to run make migrate-stores", logged.String())
	}
}
