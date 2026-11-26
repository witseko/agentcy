//go:build bench

package main

import (
	"fmt"
	"math/rand/v2"
	"strings"
)

// The vocabulary reads like agents discussing a code base, so full-text search sees a
// realistic spread of common and rare words.
var (
	nouns = []string{
		"retry", "worker", "queue", "cursor", "handler", "socket", "migration", "index",
		"schema", "token", "session", "timeout", "deadline", "snapshot", "listener", "ledger",
		"budget", "lock", "transaction", "parser", "fixture", "plugin", "assignment", "invite",
		"roster", "summary", "brief", "proposal", "handoff", "blocker", "release", "config",
		"cache", "buffer", "payload", "request", "response", "trigger", "column", "checkout",
	}
	adjectives = []string{
		"stale", "missing", "duplicate", "slow", "empty", "expired", "private", "shared",
		"nested", "partial", "unbounded", "idle", "concurrent", "malformed", "cached", "sealed",
	}
	verbs = []string{
		"fails", "retries", "blocks", "returns early", "leaks", "times out", "skips the check",
		"wakes every listener", "rereads the file", "holds the lock", "drops the row",
		"spawns a process", "logs the error", "commits twice",
	}
	actions = []string{
		"profile", "review", "rewrite", "test", "measure", "document", "split", "cache",
		"bound", "validate", "rename", "inline",
	}
	files = []string{
		"src/agentcy/mcp/core.py", "src/agentcy/mcp/server.py", "src/agentcy/conversations/repository.py",
		"src/agentcy/conversations/tasks.py", "src/agentcy/mcp/transport.py", "tui/cmd/agentcy-tui/client.go",
		"src/agentcy/storage/database.py", "src/agentcy/conversations/search.py", "tests/e2e/test_protocol_eras.py",
		"src/agentcy/mcp/assignment_worker.py", "src/agentcy/tokens/usage.py", "plugins/zai/server.py",
	}
	tests = []string{
		"test_read_after_cursor", "test_post_is_idempotent", "test_status_counts_tasks",
		"test_wait_wakes_on_post", "test_operator_search", "test_close_confirmation",
	}
)

// textGenerator produces deterministic message bodies and search queries from a seed.
type textGenerator struct {
	random *rand.Rand
}

// newTextGenerator returns a generator whose output depends only on the seed.
func newTextGenerator(seed uint64) *textGenerator {
	return &textGenerator{random: rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))}
}

// pick returns a random element of words.
func (g *textGenerator) pick(words []string) string {
	return words[g.random.IntN(len(words))]
}

// sentence returns one sentence from a random template.
func (g *textGenerator) sentence() string {
	switch g.random.IntN(6) {
	case 0:
		return fmt.Sprintf("I checked %s:%d and the %s %s when the %s is %s.",
			g.pick(files), 1+g.random.IntN(900), g.pick(nouns), g.pick(verbs), g.pick(nouns), g.pick(adjectives))
	case 1:
		return fmt.Sprintf("The %s %s in %s %s after %d attempts.",
			g.pick(adjectives), g.pick(nouns), g.pick(files), g.pick(verbs), 2+g.random.IntN(40))
	case 2:
		return fmt.Sprintf("Tests: %d passed, %d failed in %s.",
			10+g.random.IntN(300), g.random.IntN(4), g.pick(tests))
	case 3:
		return fmt.Sprintf("Next I will %s the %s and report back with evidence.", g.pick(actions), g.pick(nouns))
	case 4:
		return fmt.Sprintf("Does the %s %s the %s before the %s commits?",
			g.pick(nouns), g.pick(actions), g.pick(nouns), g.pick(nouns))
	default:
		return fmt.Sprintf("Measured %d ms p95 for the %s path with a %s %s.",
			1+g.random.IntN(500), g.pick(nouns), g.pick(adjectives), g.pick(nouns))
	}
}

// body returns a message body of one to six sentences, sometimes split into paragraphs.
func (g *textGenerator) body() string {
	count := 1 + g.random.IntN(6)
	var builder strings.Builder
	for index := range count {
		if index > 0 {
			if g.random.IntN(4) == 0 {
				builder.WriteString("\n\n")
			} else {
				builder.WriteString(" ")
			}
		}
		builder.WriteString(g.sentence())
	}
	return builder.String()
}

// query returns a search query of one or two vocabulary words.
func (g *textGenerator) query() string {
	if g.random.IntN(2) == 0 {
		return g.pick(nouns)
	}
	return g.pick(adjectives) + " " + g.pick(nouns)
}
