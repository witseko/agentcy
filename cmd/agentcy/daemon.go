package main

import (
	"context"
	"errors"
	"log"
	"os"

	"github.com/witseko/agentcy/internal/conversation"
	"github.com/witseko/agentcy/internal/core"
	"github.com/witseko/agentcy/internal/fresh"
	"github.com/witseko/agentcy/internal/paths"
)

// runCore serves the core daemon: `agentcy core`. It is the port of main in
// the former Python core's mcp/core.py and takes no arguments; AGENTCY_HOME selects the data
// directory. Log lines go to standard error, which the starting shim sends to core.log.
func runCore(arguments []string) int {
	if len(arguments) != 0 {
		return fail(errors.New("agentcy core takes no arguments"))
	}
	home, homeSet := os.LookupEnv(paths.HomeVariable)
	dataDir, err := paths.DataDir(home, homeSet)
	if err != nil {
		return fail(err)
	}
	logger := log.New(os.Stderr, "agentcy core: ", log.LstdFlags)
	shared := conversation.NewShared(dataDir, logger)
	worker := fresh.NewWorker(fresh.WorkerConfig{
		Store:    conversation.NewAssignmentStore(shared),
		Provider: &fresh.Client{DataDir: dataDir, Binary: ownExecutable()},
		Plugins:  shared.Plugins.Plugin,
		Logger:   logger,
	})
	handler := conversation.NewHandler(shared, os.Environ(), worker)
	runErr := core.Run(context.Background(), handler, core.Options{
		DataDir: dataDir, Log: os.Stderr, Background: freshRunsInBackground(worker),
	})
	if err := errors.Join(runErr, shared.Stores.Close()); err != nil {
		return fail(err)
	}
	return 0
}

// freshRunsInBackground adapts the fresh-run worker to the daemon's background hook.
func freshRunsInBackground(worker *fresh.Worker) func(ctx context.Context, wake func(core.Room)) {
	return func(ctx context.Context, wake func(core.Room)) {
		worker.Run(ctx, func(workspace, room string) {
			wake(core.Room{Workspace: workspace, Conversation: room})
		})
	}
}
