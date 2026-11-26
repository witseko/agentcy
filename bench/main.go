//go:build bench

// Command bench measures one agentcy shim and daemon implementation.
//
// AGENTCY_CORE_CMD names the daemon command and AGENTCY_BIN the shim executable, so the
// same driver measures any build of the core. The driver always
// works in a fresh temporary AGENTCY_HOME and workspace, and it stops every process it
// started before it exits. See bench/README.md for the meaning of each number.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// config holds the command-line flags and the two implementation commands.
type config struct {
	messages    int
	samples     int
	listeners   int
	seconds     int
	coldRuns    int
	postEvery   int
	jsonPath    string
	coreCommand []string
	shimPath    string
}

func main() {
	os.Exit(run())
}

// run parses the configuration, runs every measurement, and prints the report.
// It returns the process exit code, so deferred cleanup runs before the program exits.
func run() int {
	cfg, err := parseConfig()
	if err != nil {
		fmt.Fprintln(os.Stderr, "bench:", err)
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	s, err := newSession(cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, "bench:", err)
		return 1
	}
	defer s.close()

	result, err := measureAll(ctx, s)
	if err != nil {
		fmt.Fprintln(os.Stderr, "bench:", err)
		return 1
	}
	printReport(os.Stdout, result)
	if cfg.jsonPath != "" {
		if err := writeJSONReport(cfg.jsonPath, result); err != nil {
			fmt.Fprintln(os.Stderr, "bench:", err)
			return 1
		}
		fmt.Printf("\nJSON report written to %s\n", cfg.jsonPath)
	}
	return 0
}

// parseConfig reads the flags and the AGENTCY_CORE_CMD and AGENTCY_BIN variables.
func parseConfig() (config, error) {
	var cfg config
	flag.IntVar(&cfg.messages, "messages", 100000, "messages in the benchmark conversation")
	flag.IntVar(&cfg.samples, "samples", 1000, "timed requests per warm latency operation")
	flag.IntVar(&cfg.listeners, "listeners", 20, "listening shims in the load scenario")
	flag.IntVar(&cfg.seconds, "seconds", 60, "length of the load scenario in seconds")
	flag.IntVar(&cfg.coldRuns, "coldruns", 5, "cold and warm shim start runs")
	flag.IntVar(&cfg.postEvery, "postevery", 5, "seconds between writer posts in the load scenario")
	flag.StringVar(&cfg.jsonPath, "json", "", "also write the report as JSON to this file")
	flag.Parse()

	if cfg.messages < 10 || cfg.samples < 1 || cfg.listeners < 1 || cfg.seconds < 1 ||
		cfg.coldRuns < 1 || cfg.postEvery < 1 {
		return cfg, errors.New("flags out of range: need -messages >= 10 and every other count >= 1")
	}
	coreCommand, err := parseCoreCommand(os.Getenv("AGENTCY_CORE_CMD"))
	if err != nil {
		return cfg, err
	}
	cfg.coreCommand = coreCommand
	cfg.shimPath, err = parseShimPath(os.Getenv("AGENTCY_BIN"))
	return cfg, err
}

// parseCoreCommand splits the daemon command on white space, without a shell, and resolves
// the program against PATH. Arguments cannot contain spaces.
func parseCoreCommand(value string) ([]string, error) {
	words := strings.Fields(value)
	if len(words) == 0 {
		return nil, errors.New("set AGENTCY_CORE_CMD, for example: $PWD/bin/agentcy core")
	}
	program, err := exec.LookPath(words[0])
	if err != nil {
		return nil, fmt.Errorf("AGENTCY_CORE_CMD names no executable program: %w", err)
	}
	return append([]string{program}, words[1:]...), nil
}

// parseShimPath checks that AGENTCY_BIN is the absolute path of an existing file.
func parseShimPath(value string) (string, error) {
	if value == "" {
		return "", errors.New("set AGENTCY_BIN to the absolute path of the shim, for example $PWD/.venv/bin/agentcy")
	}
	if !filepath.IsAbs(value) {
		return "", errors.New("AGENTCY_BIN must be an absolute path")
	}
	info, err := os.Stat(value)
	if err != nil || !info.Mode().IsRegular() {
		return "", errors.New("AGENTCY_BIN must name an existing file")
	}
	return value, nil
}

// measureAll runs the phases in an order that keeps each one independent of the next:
// dataset, warm latency, warm shim start, load, then cold start (which needs no daemon).
func measureAll(ctx context.Context, s *session) (report, error) {
	var result report
	progress("filling the dataset with %d messages", s.cfg.messages)
	data, err := buildDataset(ctx, s)
	if err != nil {
		return result, fmt.Errorf("dataset: %w", err)
	}
	result.Dataset = data.summary()

	progress("measuring warm latency, %d samples per operation", s.cfg.samples)
	result.Latency, err = measureWarmLatency(ctx, s, data)
	if err != nil {
		return result, fmt.Errorf("warm latency: %w", err)
	}

	progress("measuring warm shim start, %d runs", s.cfg.coldRuns)
	warm, err := measureWarmStarts(ctx, s)
	if err != nil {
		return result, fmt.Errorf("warm shim start: %w", err)
	}
	result.WarmShimStart = summarizeStarts(warm)

	progress("running the load scenario: %d listeners for %d s", s.cfg.listeners, s.cfg.seconds)
	result.Load, err = runLoadScenario(ctx, s, data)
	if err != nil {
		return result, fmt.Errorf("load scenario: %w", err)
	}

	progress("measuring cold start, %d runs", s.cfg.coldRuns)
	if err := s.stopDaemon(); err != nil {
		return result, err
	}
	cold, err := measureColdStarts(ctx, s)
	if err != nil {
		return result, fmt.Errorf("cold start: %w", err)
	}
	result.ColdStart = summarizeStarts(cold)

	result.Environment = describeEnvironment(s.cfg, data.fillMethod())
	return result, nil
}

// progress prints one status line to standard error, so standard output holds only the report.
func progress(format string, arguments ...any) {
	fmt.Fprintf(os.Stderr, "%s bench: %s\n", time.Now().Format("15:04:05"), fmt.Sprintf(format, arguments...))
}
