//go:build conformance

package conformance

import (
	"os"
	"path/filepath"
	"testing"
)

// writeSetting writes a configuration file into the daemon's data directory with a mode.
func writeSetting(s *scenario, name, content string, mode os.FileMode) {
	s.t.Helper()
	path := filepath.Join(s.daemon.home, name)
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		s.t.Fatalf("write %s: %v", name, err)
	}
	if err := os.Chmod(path, mode); err != nil {
		s.t.Fatalf("chmod %s: %v", name, err)
	}
}

// From test_operator_core.py and plugin_settings.py: plugin listings show names and models
// only, sorted by name; an unsafe or invalid plugins.json gives a generic error. Nothing here
// launches a plugin.
func TestPluginSettings(t *testing.T) {
	s := newScenario(t)
	created := s.member("lead creates", "create", fields{"brief": "Plugins", "initiator": "Lead", "invitees": []any{"Worker"}})
	lead := created.text("token")
	writeSetting(s, "plugins.json", `{"plugins": {
		"zeta": {"command": ["/usr/bin/false"], "model": "zeta/model", "environment": ["ZETA_KEY"], "timeout_seconds": 60},
		"alpha": {"command": ["/usr/bin/false", "--flag"], "model": "alpha/model"}}}`, 0o600)
	s.member("lead lists the configured plugins", "plugins", fields{"token": lead})
	s.send("operator lists the configured plugins", deleteField(s.operatorRequest("operator_plugins", fields{}), "directory"))
	writeSetting(s, "plugins.json", `{"plugins": {"alpha": {"command": ["/usr/bin/false"], "model": "alpha/model"}}}`, 0o644)
	s.member("a group-readable plugins.json is refused", "plugins", fields{"token": lead})
	writeSetting(s, "plugins.json", `{"plugins": {"alpha": {"command": ["relative"], "model": "alpha/model"}}}`, 0o600)
	s.send("a relative plugin command is refused", deleteField(s.operatorRequest("operator_plugins", fields{}), "directory"))
	writeSetting(s, "plugins.json", `{not json`, 0o600)
	s.member("invalid JSON is refused", "plugins", fields{"token": lead})
}

// From test_token_usage.py: with tokens.json, members get budgets from their model, the
// balance names overloaded and preferred models, a model's ledger spans conversations, and
// an unsafe tokens.json is ignored in status instead of failing it.
func TestTokenSettings(t *testing.T) {
	s := newScenario(t)
	writeSetting(s, "tokens.json", `{"models": {
		"model-a": {"initial_tokens": 10000},
		"Model-B": {"initial_tokens": 20000, "warn_percent": 50, "imbalance_gap_percent": 10}},
		"default_initial_tokens": 5000}`, 0o600)
	created := s.member("lead creates", "create", fields{"brief": "Budgets", "initiator": "Lead", "invitees": []any{"Alpha", "Bravo", "Plain"}, "initiator_model": "model-a"})
	lead := created.text("token")
	alpha := s.member("Alpha joins on model-a", "join", fields{"code": created.text("invitations", "Alpha"), "model": "model-a"}).text("token")
	bravo := s.member("Bravo joins on model-b (case-insensitive match)", "join", fields{"code": created.text("invitations", "Bravo"), "model": "model-b"}).text("token")
	plain := s.member("Plain joins without a model", "join", fields{"code": created.text("invitations", "Plain")}).text("token")
	s.member("Alpha reports heavy usage", "report_usage", fields{"token": alpha, "input_tokens": 6000, "output_tokens": 1500})
	s.member("Bravo reports light usage", "report_usage", fields{"token": bravo, "input_tokens": 9000, "output_tokens": 2000, "model": "model-b"})
	s.post("Plain posts, adding an estimate", plain, fields{"body": "Estimated usage only for this member."})
	s.member("lead status with usage and token balance", "status", fields{"token": lead})
	second := s.member("lead creates a second conversation", "create", fields{"brief": "Budgets two", "initiator": "Lead", "invitees": []any{"Gamma"}})
	gamma := s.member("Gamma joins on model-a", "join", fields{"code": second.text("invitations", "Gamma"), "model": "model-a"}).text("token")
	s.member("Gamma reports usage", "report_usage", fields{"token": gamma, "input_tokens": 100, "output_tokens": 50})
	s.member("the second lead's status shares the model-a ledger", "status", fields{"token": second.text("token")})
	writeSetting(s, "tokens.json", `{"models": {}}`, 0o644)
	s.member("an unsafe tokens.json is ignored in status", "status", fields{"token": lead})
	writeSetting(s, "tokens.json", `{"models": {"model-a": {"initial_tokens": 0}}}`, 0o600)
	s.member("an invalid tokens.json is ignored in report_usage", "report_usage", fields{"token": alpha, "input_tokens": 7000, "output_tokens": 1500})
}
