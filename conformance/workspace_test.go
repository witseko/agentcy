//go:build conformance

package conformance

import (
	"os"
	"path/filepath"
	"testing"
)

// From src/agentcy/storage/database.py and test_status.py: in a git repository the
// workspace key is the SHA-256 of the resolved common git directory, so a subdirectory and
// a linked worktree share the store, and member requests with that key reach it.
func TestGitWorkspaceKey(t *testing.T) {
	s := newGitScenario(t)
	runGit(t, s.directory, "-c", "user.name=Conformance", "-c", "user.email=conformance@example.invalid",
		"commit", "-q", "--allow-empty", "-m", "init")
	subdirectory := filepath.Join(s.directory, "sub", "dir")
	if err := os.MkdirAll(subdirectory, 0o700); err != nil {
		t.Fatalf("make subdirectory: %v", err)
	}
	linked := filepath.Join(filepath.Dir(s.directory), "linked")
	runGit(t, s.directory, "worktree", "add", "-q", linked)
	created := s.operator("human creates from the main checkout", "operator_create", fields{"brief": "Git workspace", "lead": "Lead", "invitees": []any{"Worker"}})
	s.member("lead joins with the key of the common git directory", "join", fields{"code": created.invitationCode("Lead")})
	s.operator("list from a subdirectory", "operator_list", fields{"directory": subdirectory})
	s.operator("list from a linked worktree", "operator_list", fields{"directory": linked})
	s.member("a key of the checkout path is another workspace", "join", fields{
		"code": created.invitationCode("Worker"), "workspace": sha256Hex(resolvePath(t, s.directory)),
	})
	s.member("the worker joins with the right key", "join", fields{"code": created.invitationCode("Worker")})
}

// From test_status.py: a token from one workspace is unknown in another, and a plain
// directory's key is the SHA-256 of its resolved path, symlinks included.
func TestPlainWorkspaceKey(t *testing.T) {
	s := newScenario(t)
	other := plainWorkspace(t, s.normalizer, "<other-checkout>")
	link := filepath.Join(shortTempDir(t, "agentcy-link-"), "checkout")
	if err := os.Symlink(s.directory, link); err != nil {
		t.Fatalf("make symlink: %v", err)
	}
	s.normalizer.addLiteral(link, "<checkout-symlink>")
	created := s.operator("human creates", "operator_create", fields{"brief": "Plain workspace", "lead": "Lead", "invitees": []any{"Worker"}})
	lead := s.member("lead joins", "join", fields{"code": created.invitationCode("Lead")}).text("token")
	s.operator("list through a symlink to the checkout", "operator_list", fields{"directory": link})
	s.member("status in the right workspace", "status", fields{"token": lead})
	s.member("status in another workspace", "status", fields{"token": lead, "workspace": other.workspace})
	s.operator("list in the other checkout", "operator_list", fields{"directory": other.directory})
	s.member("a chat in the other workspace creates its own conversation", "create", fields{
		"workspace": other.workspace, "brief": "Other", "initiator": "Lead", "invitees": []any{"Worker"},
	})
	s.operator("list in the other checkout shows it", "operator_list", fields{"directory": other.directory})
	s.operator("list in the first checkout is unchanged", "operator_list", fields{})
}
