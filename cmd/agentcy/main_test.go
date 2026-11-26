package main

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestUmaskIsSetAtStart(t *testing.T) {
	previous := syscall.Umask(0o022)
	defer syscall.Umask(previous)
	run([]string{"--version"})
	if current := syscall.Umask(0o022); current != 0o077 {
		t.Errorf("umask = %o, want 077", current)
	}
}

func TestHTTPTokenLeavesTheEnvironmentBeforeAnythingStarts(t *testing.T) {
	t.Setenv("AGENTCY_HTTP_TOKEN", "e2e-access-token-00000000000000000000000000000000")
	if status := run([]string{"--workspace", t.TempDir(), "--http", "0"}); status != exitUsage {
		t.Errorf("status = %d, want %d", status, exitUsage)
	}
	if _, found := os.LookupEnv("AGENTCY_HTTP_TOKEN"); found {
		t.Error("the access token is still in the environment")
	}
}

func TestFreshWorkerRemovesTheJoinCodeFromTheEnvironment(t *testing.T) {
	t.Setenv("AGENTCY_FRESH_JOIN_CODE", "secret-code")
	if status := run([]string{"fresh-worker", filepath.Join(t.TempDir(), "missing")}); status != 1 {
		t.Errorf("status = %d, want 1", status)
	}
	if _, found := os.LookupEnv("AGENTCY_FRESH_JOIN_CODE"); found {
		t.Error("the join code is still in the environment")
	}
}

func TestPublicHostNeedsHTTP(t *testing.T) {
	if status := run([]string{"--workspace", t.TempDir(), "--http-public-host", "bridge.example.com"}); status != exitUsage {
		t.Errorf("status = %d, want %d", status, exitUsage)
	}
	if status := run([]string{"--http", "8765"}); status != exitUsage {
		t.Errorf("missing workspace: status = %d", status)
	}
}

func TestParsePortAcceptsOnlyDecimalIntegers(t *testing.T) {
	for _, value := range []string{"8765", "1", "70000"} {
		if _, err := parsePort(value); err != nil {
			t.Errorf("%q: %v", value, err)
		}
	}
	for _, value := range []string{"", "x", "80x", "0x10"} {
		if _, err := parsePort(value); err == nil {
			t.Errorf("%q accepted", value)
		}
	}
}
