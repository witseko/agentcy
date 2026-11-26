//go:build conformance

package conformance

import (
	"path/filepath"
	"strings"
	"testing"
)

// operatorExamples returns one valid request body per operator action, without the envelope.
func operatorExamples(conversation string) map[string]fields {
	return map[string]fields{
		"operator_plugins":           {},
		"operator_list":              {},
		"operator_show":              {"conversation": conversation},
		"operator_tasks":             {"conversation": conversation},
		"operator_create":            {"brief": "Keyed", "lead": "Lead", "invitees": []any{"W"}},
		"operator_invite":            {"conversation": conversation, "name": "Extra"},
		"operator_open_invite":       {"conversation": conversation},
		"operator_launch":            {"conversation": conversation, "name": "W", "plugin": "fixture"},
		"operator_retire":            {"conversation": conversation, "name": "W"},
		"operator_set_role":          {"conversation": conversation, "name": "W", "role": "co-lead"},
		"operator_post":              {"conversation": conversation, "submission_id": "k", "body": "Hi"},
		"operator_close":             {"conversation": conversation},
		"operator_reopen":            {"conversation": conversation},
		"operator_archive":           {"conversation": conversation, "archived": false},
		"operator_rename":            {"conversation": conversation, "title": "T"},
		"operator_search":            {"query": "keyed"},
		"operator_set_member_budget": {"conversation": conversation, "name": "W", "token_budget": 10},
	}
}

// operatorActions lists the 17 operator actions in a fixed order for the transcript.
var operatorActions = []string{
	"operator_plugins", "operator_list", "operator_show", "operator_tasks", "operator_create",
	"operator_invite", "operator_open_invite", "operator_launch", "operator_retire",
	"operator_set_role", "operator_post", "operator_close", "operator_reopen", "operator_archive",
	"operator_rename", "operator_search", "operator_set_member_budget",
}

// From test_operator_core.py: every operator action rejects a missing, wrong, too long,
// empty or non-ASCII key with one generic error, and nothing changes.
func TestOperatorKeyRejection(t *testing.T) {
	s := newScenario(t)
	created := s.operator("human creates", "operator_create", fields{"brief": "Keyed", "lead": "Lead", "invitees": []any{"W"}})
	conversation := created.text("conversation")
	memberToken := s.member("lead joins", "join", fields{"code": created.invitationCode("Lead")}).text("token")
	wrongKeys := []string{"wrong", memberToken, strings.Repeat("k", 200), "", "schl\u00fcssel"}
	examples := operatorExamples(conversation)
	for _, action := range operatorActions {
		request := s.operatorRequest(action, examples[action])
		if action == "operator_plugins" {
			delete(request, "directory")
		}
		missing := copyFields(request)
		delete(missing, "key")
		s.send(action+" without a key", missing)
		for _, key := range wrongKeys {
			attempt := copyFields(request)
			attempt["key"] = key
			s.send(action+" with a wrong key", attempt)
		}
	}
	s.operator("list shows one conversation with one joined slot", "operator_list", fields{})
}

// From test_operator_core.py: which error wins when a request breaks several rules at once.
func TestOperatorErrorPrecedence(t *testing.T) {
	s := newScenario(t)
	created := s.operator("human creates", "operator_create", fields{"brief": "Precedence", "lead": "Lead", "invitees": []any{"W"}})
	conversation := created.text("conversation")
	wrongKeyBadField := s.operatorRequest("operator_show", fields{"conversation": "not-an-id"})
	wrongKeyBadField["key"] = "wrong"
	s.send("a wrong key and an invalid conversation ID", wrongKeyBadField)
	longKeyBadField := s.operatorRequest("operator_show", fields{"conversation": "not-an-id"})
	longKeyBadField["key"] = strings.Repeat("k", 200)
	s.send("a too-long key and an invalid conversation ID", longKeyBadField)
	missingKeyNoDirectory := s.operatorRequest("operator_list", fields{})
	delete(missingKeyNoDirectory, "key")
	delete(missingKeyNoDirectory, "directory")
	s.send("no key and no directory", missingKeyNoDirectory)
	wrongKeyUnknown := s.operatorRequest("operator_show", fields{"conversation": "0123456789abcdef01234567"})
	wrongKeyUnknown["key"] = "wrong"
	s.send("a wrong key and an unknown conversation", wrongKeyUnknown)
	wrongKeyMissingDirectory := s.operatorRequest("operator_list", fields{"directory": filepath.Join(s.directory, "missing")})
	wrongKeyMissingDirectory["key"] = "wrong"
	s.send("a wrong key and a missing directory", wrongKeyMissingDirectory)
	versionTwo := s.operatorRequest("operator_list", fields{"version": 2})
	s.send("version 2", versionTwo)
	versionString := s.operatorRequest("operator_list", fields{"version": "1"})
	s.send("version as a string", versionString)
	s.send("no version", deleteField(s.operatorRequest("operator_list", fields{}), "version"))
	s.operator("an unknown field", "operator_list", fields{"extra": true})
	s.operator("an unknown operator action", "operator_unknown", fields{})
	s.operator("a relative directory", "operator_list", fields{"directory": "work"})
	s.operator("a missing directory", "operator_list", fields{"directory": filepath.Join(s.directory, "missing")})
	s.operator("a file instead of a directory", "operator_list", fields{"directory": "/etc/hosts"})
	s.operator("a directory with a NUL byte", "operator_list", fields{"directory": s.directory + "\x00"})
	s.operator("a missing directory for an unknown conversation", "operator_show", fields{"directory": filepath.Join(s.directory, "missing"), "conversation": "0123456789abcdef01234567"})
	s.operator("the human cannot post a task", "operator_post", fields{"conversation": conversation, "submission_id": "p", "body": "Hi", "kind": "task", "to": "W"})
	s.send("a member request with an operator key", fields{"action": "status", "workspace": s.workspace, "token": "t", "key": "k"})
	s.send("a member request without a token", fields{"action": "status", "workspace": s.workspace})
}

// From test_operator.py and test_operator_core.py: a checkout without a store lists and
// searches nothing and refuses everything else, without creating a store.
func TestOperatorWithoutStore(t *testing.T) {
	s := newScenario(t)
	unknown := "0123456789abcdef01234567"
	s.operator("list without a store", "operator_list", fields{})
	s.operator("archived list without a store", "operator_list", fields{"archived": true})
	s.operator("search without a store", "operator_search", fields{"query": "retry"})
	s.send("plugins without a configuration", deleteField(s.operatorRequest("operator_plugins", fields{}), "directory"))
	for _, action := range operatorActions {
		if action == "operator_list" || action == "operator_search" || action == "operator_plugins" || action == "operator_create" {
			continue
		}
		s.operator(action+" without a store", action, operatorExamples(unknown)[action])
	}
	s.member("list_closed without a store", "list_closed", fields{})
	s.member("mcp_task_status without a store", "mcp_task_status", fields{"task_id": "task-1"})
	s.member("mcp_task_cancel without a store", "mcp_task_cancel", fields{"task_id": "task-1"})
	s.member("status with an unknown token creates a store", "status", fields{"token": "unknown"})
	s.operator("list after a store exists", "operator_list", fields{})
	s.operator("show an unknown conversation in an existing store", "operator_show", fields{"conversation": unknown})
}

// From test_operator_core.py: plugin listing and the launch checks that run before any
// plugin is involved. No plugins.json is configured, so launches cannot start.
func TestPluginsLaunchAndMcpTasks(t *testing.T) {
	s := newScenario(t)
	created := s.member("lead creates", "create", fields{"brief": "Plugins", "initiator": "Lead", "invitees": []any{"Worker"}})
	lead := created.text("token")
	worker := s.member("worker joins", "join", fields{"code": created.text("invitations", "Worker")}).text("token")
	s.member("lead lists plugins", "plugins", fields{"token": lead})
	s.member("a worker cannot list plugins", "plugins", fields{"token": worker})
	s.member("an unknown token cannot list plugins", "plugins", fields{"token": "unknown"})
	s.member("a worker cannot launch", "launch", fields{"token": worker, "name": "Helper", "plugin": "fixture", "directory": s.directory})
	s.member("launch with an invalid plugin name", "launch", fields{"token": lead, "name": "Helper", "plugin": "bad plugin", "directory": s.directory})
	s.member("mcp_task_status of an unknown task", "mcp_task_status", fields{"task_id": "task-1"})
	s.member("mcp_task_cancel of an unknown task", "mcp_task_cancel", fields{"task_id": "task-1"})
	s.member("an invalid MCP task ID", "mcp_task_status", fields{"task_id": "bad id!"})
	s.member("a 129-character MCP task ID", "mcp_task_status", fields{"task_id": strings.Repeat("t", 129)})
	s.send("operator plugins", deleteField(s.operatorRequest("operator_plugins", fields{}), "directory"))
	s.operator("operator plugins refuses a directory", "operator_plugins", fields{})
	s.member("lead closes", "close", fields{"token": lead})
	s.member("lead cannot list plugins after close", "plugins", fields{"token": lead})
}

func copyFields(request fields) fields {
	copied := fields{}
	for key, value := range request {
		copied[key] = value
	}
	return copied
}

func deleteField(request fields, key string) fields {
	delete(request, key)
	return request
}
