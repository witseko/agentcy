// Command agentcy is the per-chat MCP shim of the agentcy bridge, the fresh worker, and
// the launcher of the terminal UI. It accepts the flags of the Python command
// (src/agentcy/tools/cli.py) so existing host registrations keep working.
package main

import (
	"fmt"
	"os"
	"syscall"
)

// version is the agentcy release, set at build time with -ldflags "-X main.version=...".
var version = "0.0.0-dev"

// exitUsage is argparse's exit code for a usage error.
const exitUsage = 2

// main dispatches to the subcommand and exits with its status.
func main() {
	os.Exit(run(os.Args[1:]))
}

// run dispatches on the first argument and returns the exit status.
func run(arguments []string) int {
	// Files and sockets this process creates are private to the user.
	syscall.Umask(0o077)
	if len(arguments) > 0 {
		switch arguments[0] {
		case "ui":
			return runUI(arguments[1:])
		case "fresh-worker":
			return runFreshWorker(arguments[1:])
		}
	}
	return runShim(arguments)
}

// fail prints an error the way the Python command does and returns status 1.
func fail(err error) int {
	fmt.Fprintln(os.Stderr, err)
	return 1
}
