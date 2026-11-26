package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"syscall"

	"github.com/witseko/agentcy/internal/paths"
	"github.com/witseko/agentcy/internal/shim"
)

// shimDescription is the command description of `agentcy --help`.
const shimDescription = "Launch the bridge for a desktop app or CLI over stdio or HTTP, or the terminal UI."

// shimOptions are the command-line options of the shim.
type shimOptions struct {
	workspace   string
	httpPort    int
	httpSet     bool
	publicHost  string
	hostSet     bool
	showVersion bool
}

// parseShimOptions parses the shim flags. Go's flag package accepts the Python
// command's `--flag VALUE` and `--flag=VALUE` forms; argparse's unique-prefix
// abbreviations are not accepted.
func parseShimOptions(arguments []string, output io.Writer) (shimOptions, error) {
	var options shimOptions
	flags := flag.NewFlagSet("agentcy", flag.ContinueOnError)
	flags.SetOutput(output)
	flags.Usage = func() {
		fmt.Fprintf(output, "usage: agentcy [--version] --workspace WORKSPACE [--http PORT] [--http-public-host HOST]\n\n%s\n", shimDescription)
		flags.PrintDefaults()
	}
	flags.StringVar(&options.workspace, "workspace", "", "directory shared by the apps and CLIs")
	flags.Func("http", "serve MCP over HTTP on 127.0.0.1:PORT in place of stdio, for a remote chat behind a tunnel; needs a secret in AGENTCY_HTTP_TOKEN", func(value string) error {
		port, err := parsePort(value)
		options.httpPort, options.httpSet = port, true
		return err
	})
	flags.Func("http-public-host", "host name of the tunnel that forwards to the HTTP port, for example bridge.example.com", func(value string) error {
		options.publicHost, options.hostSet = value, true
		return nil
	})
	flags.BoolVar(&options.showVersion, "version", false, "show the version and exit")
	if err := flags.Parse(arguments); err != nil {
		return options, err
	}
	if flags.NArg() > 0 {
		return options, fmt.Errorf("unrecognized arguments: %v", flags.Args())
	}
	return options, nil
}

// parsePort reads a decimal port number.
func parsePort(value string) (int, error) {
	port, err := strconv.Atoi(value)
	if err != nil || strconv.Itoa(port) != value {
		return 0, fmt.Errorf("invalid int value: %q", value)
	}
	return port, nil
}

// usageError prints an argparse-style error and returns its exit status.
func usageError(message string) int {
	fmt.Fprintf(os.Stderr, "usage: agentcy [--version] --workspace WORKSPACE [--http PORT] [--http-public-host HOST]\nagentcy: error: %s\n", message)
	return exitUsage
}

// runShim starts the shim for the workspace chosen in the host's MCP settings.
func runShim(arguments []string) int {
	options, err := parseShimOptions(arguments, os.Stderr)
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}
	if err != nil {
		return usageError(err.Error())
	}
	if options.showVersion {
		fmt.Printf("agentcy %s\n", version)
		return 0
	}
	if options.workspace == "" {
		return usageError("the following arguments are required: --workspace")
	}
	if !options.httpSet && options.hostSet {
		return usageError("--http-public-host needs --http")
	}
	var settings shim.HTTPSettings
	if options.httpSet {
		// The core and the launched agents inherit this environment, so the token
		// leaves it before anything is started.
		token := os.Getenv(shim.AccessTokenVariable)
		os.Unsetenv(shim.AccessTokenVariable)
		settings, err = shim.ReadHTTPSettings(options.httpPort, options.publicHost, options.hostSet, token)
		if err != nil {
			return usageError(err.Error())
		}
	}
	directory, err := paths.RealPath(options.workspace, true)
	if err != nil {
		return fail(err)
	}
	key, err := paths.WorkspaceKey(directory, os.Environ())
	if err != nil {
		return fail(err)
	}
	server, err := shim.NewServer(shim.Config{
		Workspace: key, Directory: directory, Version: version, Core: coreClient(), OverHTTP: options.httpSet,
	})
	if err != nil {
		return fail(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if options.httpSet {
		err = shim.ServeHTTP(ctx, server, settings)
	} else {
		err = shim.Serve(ctx, server)
	}
	if err != nil && ctx.Err() == nil {
		return fail(err)
	}
	return 0
}
