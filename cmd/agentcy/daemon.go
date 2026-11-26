package main

import (
	"context"
	"errors"
	"log"
	"os"

	"github.com/witseko/agentcy/internal/conversation"
	"github.com/witseko/agentcy/internal/core"
	"github.com/witseko/agentcy/internal/paths"
	"github.com/witseko/agentcy/internal/store"
)

// runCore serves the core daemon: `agentcy core`. It is the port of main in
// src/agentcy/mcp/core.py and takes no arguments; AGENTCY_HOME selects the data
// directory. Log lines go to standard error, which the starting shim sends to core.log.
func runCore(arguments []string) int {
	if len(arguments) != 0 {
		return fail(errorText("agentcy core takes no arguments"))
	}
	home, homeSet := os.LookupEnv(paths.HomeVariable)
	dataDir, err := paths.DataDir(home, homeSet)
	if err != nil {
		return fail(err)
	}
	logger := log.New(os.Stderr, "agentcy core: ", log.LstdFlags)
	handler := conversation.NewHandler(dataDir, os.Environ(), logger)
	runErr := core.Run(context.Background(), handler, core.Options{DataDir: dataDir, Log: os.Stderr})
	if err := errors.Join(runErr, store.CloseAll()); err != nil {
		return fail(err)
	}
	return 0
}
