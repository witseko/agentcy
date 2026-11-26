//go:build bench

package main

import (
	"context"
	"fmt"
	"math"
	"slices"
	"time"
)

// warmupRequests run before each timed operation and are not counted.
const warmupRequests = 10

// latencyStats summarizes a set of durations in milliseconds.
type latencyStats struct {
	Count int     `json:"count"`
	P50   float64 `json:"p50_ms"`
	P95   float64 `json:"p95_ms"`
	P99   float64 `json:"p99_ms"`
	Max   float64 `json:"max_ms"`
}

// namedLatency is the latency of one operation, in report order.
type namedLatency struct {
	Operation string       `json:"operation"`
	Stats     latencyStats `json:"stats"`
}

// summarize returns nearest-rank percentiles of the durations.
func summarize(durations []time.Duration) latencyStats {
	if len(durations) == 0 {
		return latencyStats{}
	}
	sorted := slices.Clone(durations)
	slices.Sort(sorted)
	return latencyStats{
		Count: len(sorted),
		P50:   milliseconds(percentile(sorted, 50)),
		P95:   milliseconds(percentile(sorted, 95)),
		P99:   milliseconds(percentile(sorted, 99)),
		Max:   milliseconds(sorted[len(sorted)-1]),
	}
}

// percentile returns the nearest-rank percentile of sorted durations.
func percentile(sorted []time.Duration, percent float64) time.Duration {
	rank := int(math.Ceil(percent / 100 * float64(len(sorted))))
	return sorted[max(rank, 1)-1]
}

// milliseconds converts a duration to fractional milliseconds.
func milliseconds(duration time.Duration) float64 {
	return float64(duration.Microseconds()) / 1000
}

// timedOperation is one request kind measured over the socket. The sample index lets a
// request vary, for example a unique submission ID per post.
type timedOperation struct {
	name    string
	request func(sample int) error
}

// measureWarmLatency times each operation over the socket with the daemon already running.
// Posts run last, so the other operations see the dataset as it was filled.
func measureWarmLatency(ctx context.Context, s *session, data *dataset) ([]namedLatency, error) {
	operator, err := newOperatorClient(s)
	if err != nil {
		return nil, err
	}
	queries := newTextGenerator(fillSeed + 2)
	readCursor := data.lastMessageID - 20
	operations := []timedOperation{
		{"member read (incremental)", func(int) error {
			_, err := exchange(s.socketPath(), s.memberRequest("read", map[string]any{
				"token": data.benchToken, "after": readCursor,
			}))
			return err
		}},
		{"member status", func(int) error {
			_, err := exchange(s.socketPath(), s.memberRequest("status", map[string]any{"token": data.benchToken}))
			return err
		}},
		{"operator_show (tail)", func(int) error {
			_, err := operator.showTail(data.conversation)
			return err
		}},
		{"operator_search", func(int) error {
			_, err := operator.call("operator_search", map[string]any{"query": queries.query()})
			return err
		}},
		{"member post", func(sample int) error {
			_, err := exchange(s.socketPath(), s.memberRequest("post", map[string]any{
				"token":         data.benchToken,
				"submission_id": fmt.Sprintf("bench-latency-%d", sample),
				"body":          fmt.Sprintf("Latency probe %d: %s", sample, queries.body()),
			}))
			return err
		}},
	}
	var results []namedLatency
	for _, operation := range operations {
		stats, err := timeOperation(ctx, operation, s.cfg.samples)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", operation.name, err)
		}
		results = append(results, namedLatency{Operation: operation.name, Stats: stats})
	}
	return results, nil
}

// timeOperation runs the warmup requests, then the timed samples one after another.
func timeOperation(ctx context.Context, operation timedOperation, samples int) (latencyStats, error) {
	durations := make([]time.Duration, 0, samples)
	for sample := range warmupRequests + samples {
		if err := ctx.Err(); err != nil {
			return latencyStats{}, err
		}
		started := time.Now()
		if err := operation.request(sample); err != nil {
			return latencyStats{}, err
		}
		if sample >= warmupRequests {
			durations = append(durations, time.Since(started))
		}
	}
	return summarize(durations), nil
}
