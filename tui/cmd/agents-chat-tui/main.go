// Command agents-chat-tui is the human's terminal UI for agents-chat. It talks to the
// running core over the operator protocol on the core's Unix socket. Start it with
// `agents-chat ui`, which makes sure the core runs and then starts this program.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	tea "charm.land/bubbletea/v2"
)

// main parses the workspace, then runs the UI until the user quits.
func main() {
	workspace := flag.String("workspace", ".", "checkout directory whose conversations to manage")
	flag.Parse()
	if err := run(*workspace); err != nil {
		fmt.Fprintln(os.Stderr, "agents-chat-tui:", err)
		os.Exit(1)
	}
}

// run resolves the checkout and the bridge data directory and runs the program.
func run(workspace string) error {
	directory, err := filepath.Abs(workspace)
	if err != nil {
		return err
	}
	if info, err := os.Stat(directory); err != nil || !info.IsDir() {
		return fmt.Errorf("workspace %s is not a directory", directory)
	}
	home, err := bridgeHome()
	if err != nil {
		return err
	}
	_, err = tea.NewProgram(newModel(NewClient(home), directory)).Run()
	return err
}
