package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	"github.com/witseko/agentcy/internal/paths"
)

// The terminal UI program and its messages, from the former Python core's tools/cli.py.
const (
	tuiProgram  = "agentcy-tui"
	tuiVariable = "AGENTCY_TUI"
	tuiMissing  = "The terminal UI is not installed. Run 'make setup' in the agentcy clone, " +
		"or set AGENTCY_TUI to the absolute path of a built agentcy-tui binary."
)

// tuiExecutable finds the UI binary from AGENTCY_TUI, beside this program (the clone's
// bin/, where `make setup` installs both), or on the absolute PATH entries.
func tuiExecutable() (string, error) {
	if configured := os.Getenv(tuiVariable); configured != "" {
		if !filepath.IsAbs(configured) || !paths.IsExecutableFile(configured) {
			return "", errors.New("AGENTCY_TUI must be the absolute path of an executable file")
		}
		return configured, nil
	}
	if exe := ownExecutable(); exe != "" {
		if candidate := filepath.Join(filepath.Dir(exe), tuiProgram); paths.IsExecutableFile(candidate) {
			return candidate, nil
		}
	}
	pathValue, pathSet := os.LookupEnv("PATH")
	if found := paths.FindOnPath(tuiProgram, pathValue, pathSet); found != "" {
		return found, nil
	}
	return "", errors.New(tuiMissing)
}

// runUI makes sure the core runs, then replaces this process with the terminal UI.
func runUI(arguments []string) int {
	workingDirectory, _ := os.Getwd()
	flags := flag.NewFlagSet("agentcy ui", flag.ContinueOnError)
	workspace := flags.String("workspace", workingDirectory, "checkout whose conversations to show (default: the current directory)")
	if err := flags.Parse(arguments); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return exitUsage
	}
	if flags.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "agentcy ui: unrecognized arguments: %v\n", flags.Args())
		return exitUsage
	}
	executable, err := tuiExecutable()
	if err != nil {
		return fail(err)
	}
	directory, err := paths.RealPath(*workspace, true)
	if err != nil {
		return fail(fmt.Errorf("Workspace not found: %s", *workspace))
	}
	if info, err := os.Stat(directory); err != nil || !info.IsDir() {
		return fail(fmt.Errorf("Workspace is not a directory: %s", directory))
	}
	connection, err := connectCore(context.Background())
	if err != nil {
		return fail(fmt.Errorf("Cannot start the bridge core: %v", err))
	}
	connection.Close()
	// The binary comes from AGENTCY_TUI or a fixed location, and receives a fixed argument
	// list with no shell and an explicit environment.
	err = syscall.Exec(executable, []string{executable, "--workspace", directory}, os.Environ())
	return fail(err)
}
