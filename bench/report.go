//go:build bench

package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"text/tabwriter"
)

// report holds every measured number. The JSON file and the printed table show the same values.
type report struct {
	Environment   environment    `json:"environment"`
	Dataset       datasetSummary `json:"dataset"`
	Latency       []namedLatency `json:"warm_latency"`
	ColdStart     startStats     `json:"cold_start"`
	WarmShimStart startStats     `json:"warm_shim_start"`
	Load          loadResult     `json:"load"`
}

// printReport writes the human-readable report.
func printReport(output io.Writer, r report) {
	env := r.Environment
	fmt.Fprintln(output, "agentcy benchmark")
	fmt.Fprintln(output)
	table := tabwriter.NewWriter(output, 0, 0, 2, ' ', 0)
	fmt.Fprintf(table, "Date\t%s\n", env.Date)
	fmt.Fprintf(table, "OS\t%s (%s)\n", env.OS, env.Kernel)
	fmt.Fprintf(table, "CPU\t%s, %d cores\n", env.CPUModel, env.Cores)
	fmt.Fprintf(table, "Go\t%s\n", env.GoVersion)
	fmt.Fprintf(table, "Core command\t%s\n", env.CoreCommand)
	fmt.Fprintf(table, "Shim\t%s\n", env.ShimPath)
	fmt.Fprintf(table, "Dataset\t%d messages in one conversation, %d listener slots\n", r.Dataset.Messages, r.Dataset.ListenerSlots)
	fmt.Fprintf(table, "Fill method\t%s\n", env.FillMethod)
	table.Flush()

	fmt.Fprintln(output)
	fmt.Fprintln(output, "Warm latency over the socket (ms)")
	table = tabwriter.NewWriter(output, 0, 0, 2, ' ', tabwriter.AlignRight)
	fmt.Fprintln(table, "operation\tsamples\tp50\tp95\tp99\tmax\t")
	for _, entry := range r.Latency {
		fmt.Fprintf(table, "%s\t%d\t%.2f\t%.2f\t%.2f\t%.2f\t\n", entry.Operation, entry.Stats.Count,
			entry.Stats.P50, entry.Stats.P95, entry.Stats.P99, entry.Stats.Max)
	}
	table.Flush()

	fmt.Fprintln(output)
	fmt.Fprintln(output, "Shim start to first tool result (ms)")
	table = tabwriter.NewWriter(output, 0, 0, 2, ' ', tabwriter.AlignRight)
	fmt.Fprintln(table, "start\truns\tmin\tmedian\tmax (bound)\t")
	printStart(table, "cold (no daemon)", r.ColdStart)
	printStart(table, "warm (daemon running)", r.WarmShimStart)
	table.Flush()

	printLoad(output, r.Load)
}

// printStart writes one shim start row.
func printStart(table io.Writer, name string, stats startStats) {
	fmt.Fprintf(table, "%s\t%d\t%.1f\t%.1f\t%.1f\t\n", name, stats.Runs, stats.Min, stats.Median, stats.Max)
}

// printLoad writes the load scenario section.
func printLoad(output io.Writer, load loadResult) {
	fmt.Fprintln(output)
	fmt.Fprintf(output, "Load scenario: %d listening shims, 1 poller at 1 Hz, 1 writer, %d s\n", load.Listeners, load.Seconds)
	table := tabwriter.NewWriter(output, 0, 0, 2, ' ', 0)
	fmt.Fprintf(table, "Processes measured\t%d (daemon process group plus shims)\n", load.Processes)
	fmt.Fprintf(table, "CPU seconds, total\t%.2f\n", load.CPUSeconds)
	fmt.Fprintf(table, "CPU seconds, daemon\t%.2f\n", load.DaemonCPUSeconds)
	fmt.Fprintf(table, "CPU seconds, shims\t%.2f\n", load.ShimCPUSeconds)
	fmt.Fprintf(table, "Peak RSS, total\t%.1f MB\n", load.PeakRSSMB)
	fmt.Fprintf(table, "Peak RSS, daemon\t%.1f MB\n", load.DaemonPeakRSSMB)
	if load.GitSpawns >= 0 {
		fmt.Fprintf(table, "git processes spawned by the daemon\t%d\n", load.GitSpawns)
	} else {
		fmt.Fprintf(table, "git processes spawned by the daemon\tnot measured\n")
	}
	fmt.Fprintf(table, "Polls (errors)\t%d (%d)\n", load.Polls, load.PollErrors)
	fmt.Fprintf(table, "Probes posted (errors)\t%d (%d)\n", load.Probes, load.PostErrors)
	fmt.Fprintf(table, "Wakes observed / expected\t%d / %d\n", load.WakesObserved, load.WakesExpected)
	fmt.Fprintf(table, "Listener errors\t%d\n", load.ListenerErrors)
	fmt.Fprintf(table, "Wake latency p50 / p95\t%.1f / %.1f ms\n", load.WakeLatency.P50, load.WakeLatency.P95)
	table.Flush()
}

// writeJSONReport writes the report as indented JSON with owner-only permissions.
func writeJSONReport(path string, r report) error {
	content, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(content, '\n'), 0o600)
}
