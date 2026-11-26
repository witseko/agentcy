//go:build bench

package main

import (
	"context"
	"errors"
	"slices"
	"time"
)

// startStats summarizes shim start runs in milliseconds. Max is the stated bound.
type startStats struct {
	Runs   int     `json:"runs"`
	Min    float64 `json:"min_ms"`
	Median float64 `json:"median_ms"`
	Max    float64 `json:"max_ms"`
}

// summarizeStarts returns the minimum, median (lower middle), and maximum of the runs.
func summarizeStarts(durations []time.Duration) startStats {
	if len(durations) == 0 {
		return startStats{}
	}
	sorted := slices.Clone(durations)
	slices.Sort(sorted)
	return startStats{
		Runs:   len(sorted),
		Min:    milliseconds(sorted[0]),
		Median: milliseconds(sorted[(len(sorted)-1)/2]),
		Max:    milliseconds(sorted[len(sorted)-1]),
	}
}

// firstToolResult starts a shim and times it until the first tool result: initialize,
// notifications/initialized, and one list_closed_conversations call, which needs no
// token but opens the workspace store. The shim keeps running for the caller to stop.
func firstToolResult(s *session) (*shimProcess, time.Duration, error) {
	started := time.Now()
	shim, err := startShim(s)
	if err != nil {
		return nil, 0, err
	}
	if err := shim.initialize(); err != nil {
		shim.stop()
		return nil, 0, err
	}
	if _, err := shim.callTool("list_closed_conversations", map[string]any{}); err != nil {
		shim.stop()
		return nil, 0, err
	}
	return shim, time.Since(started), nil
}

// measureWarmStarts times shim starts while the benchmark daemon is running.
func measureWarmStarts(ctx context.Context, s *session) ([]time.Duration, error) {
	var durations []time.Duration
	for range s.cfg.coldRuns {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		shim, elapsed, err := firstToolResult(s)
		if err != nil {
			return nil, err
		}
		shim.stop()
		durations = append(durations, elapsed)
	}
	return durations, nil
}

// measureColdStarts times shim starts with no daemon running, so each shim starts its own
// daemon the way it does for a user. Between runs the benchmark stops that daemon: it
// finds the daemon through core.lock while the shim still runs, stops the shim, then
// stops the daemon.
func measureColdStarts(ctx context.Context, s *session) ([]time.Duration, error) {
	var durations []time.Duration
	for range s.cfg.coldRuns {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		holders, err := s.lockHolders()
		if err != nil {
			return nil, err
		}
		if len(holders) > 0 {
			return nil, errors.New("a daemon is still running before a cold start")
		}
		shim, elapsed, err := firstToolResult(s)
		if err != nil {
			return nil, err
		}
		daemons, err := s.lockHolders()
		shim.stop()
		if err != nil {
			return nil, err
		}
		for _, pid := range daemons {
			if err := stopProcess(pid); err != nil {
				return nil, err
			}
		}
		durations = append(durations, elapsed)
	}
	return durations, nil
}
