package main

import (
	"context"
	"errors"
	"os"
	"os/signal"
	"syscall"

	"github.com/witseko/agentcy/internal/paths"
	"github.com/witseko/agentcy/internal/shim"
)

// runFreshWorker serves the fresh worker of one assignment on stdio:
// `agentcy fresh-worker DIR`, with the join code in AGENTCY_FRESH_JOIN_CODE.
// Port of the __main__ block of src/agentcy/mcp/fresh_server.py.
func runFreshWorker(arguments []string) int {
	if len(arguments) != 1 {
		return fail(errors.New("Worker requires its configured workspace"))
	}
	code, found := os.LookupEnv(shim.FreshJoinCodeVariable)
	// The code leaves the environment before Git or the core can inherit it.
	os.Unsetenv(shim.FreshJoinCodeVariable)
	if !found {
		return fail(errors.New("Worker requires a private join credential"))
	}
	key, err := paths.WorkspaceKey(arguments[0], os.Environ())
	if err != nil {
		return fail(err)
	}
	server, err := shim.NewFreshWorkerServer(key, code, coreClient())
	if err != nil {
		return fail(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := shim.Serve(ctx, server); err != nil && ctx.Err() == nil {
		return fail(err)
	}
	return 0
}
