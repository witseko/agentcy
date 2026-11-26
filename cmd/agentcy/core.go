package main

import (
	"context"
	"net"
	"os"
	"path/filepath"

	"github.com/witseko/agentcy/internal/paths"
	"github.com/witseko/agentcy/internal/wire"
)

// coreClient returns a client for the core daemon. The data directory is checked on each
// connection, as the Python shim does, so a bad AGENTCY_HOME fails the tool call rather
// than the start. The core inherits the environment at the time it is started.
func coreClient() *wire.Client {
	return &wire.Client{Connect: connectCore}
}

// coreDataDir resolves and validates the configured bridge data directory.
func coreDataDir() (string, error) {
	home, homeSet := os.LookupEnv(paths.HomeVariable)
	return paths.DataDir(home, homeSet)
}

// connectCore connects to the core of the configured data directory, starting it when
// none listens.
func connectCore(ctx context.Context) (net.Conn, error) {
	dataDir, err := coreDataDir()
	if err != nil {
		return nil, err
	}
	connector := &wire.Connector{DataDir: dataDir, Environment: os.Environ(), Command: coreCommand}
	return connector.Connect(ctx)
}

// dialCorePassive connects to an already-running core socket without starting a daemon.
func dialCorePassive(ctx context.Context) (net.Conn, error) {
	dataDir, err := coreDataDir()
	if err != nil {
		return nil, err
	}
	dialer := net.Dialer{}
	return dialer.DialContext(ctx, "unix", filepath.Join(dataDir, wire.SocketFileName))
}

// coreCommand returns the daemon command: AGENTCY_CORE_BIN when set, else this program
// run as `agentcy core` (see wire.CoreCommand).
func coreCommand() ([]string, error) {
	coreBin, coreBinSet := os.LookupEnv(wire.CoreBinVariable)
	return wire.CoreCommand(coreBin, coreBinSet, ownExecutable())
}

// ownExecutable returns the path of this program with symbolic links resolved, or "" when
// it cannot be found.
func ownExecutable() string {
	executable, err := os.Executable()
	if err != nil {
		return ""
	}
	resolved, err := filepath.EvalSymlinks(executable)
	if err != nil {
		return executable
	}
	return resolved
}
