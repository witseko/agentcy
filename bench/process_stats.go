//go:build bench

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

// linuxClockTicks is USER_HZ, the unit of CPU times in /proc/<pid>/stat on Linux.
const linuxClockTicks = 100

// processInfo is one process at one moment: its process group, resident memory, and
// cumulative CPU time (user plus system) of the process itself.
type processInfo struct {
	group      int
	rssKB      int64
	cpuSeconds float64
}

// sampleProcesses returns every visible process by PID. On Linux it reads /proc, because
// ps reports CPU time there in whole seconds. Elsewhere (macOS) it runs ps once, which
// reports CPU time in hundredths of a second.
func sampleProcesses() (map[int]processInfo, error) {
	if runtime.GOOS == "linux" {
		return sampleProcFS()
	}
	return samplePS()
}

// samplePS parses `ps -A -o pid=,pgid=,rss=,time=`. RSS is in KiB.
func samplePS() (map[int]processInfo, error) {
	output, err := exec.Command("ps", "-A", "-o", "pid=,pgid=,rss=,time=").Output()
	if err != nil {
		return nil, fmt.Errorf("ps: %w", err)
	}
	processes := map[int]processInfo{}
	for _, line := range strings.Split(string(output), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 4 {
			continue
		}
		pid, pidErr := strconv.Atoi(fields[0])
		group, groupErr := strconv.Atoi(fields[1])
		rss, rssErr := strconv.ParseInt(fields[2], 10, 64)
		cpu, cpuErr := parseCPUTime(fields[3])
		if pidErr != nil || groupErr != nil || rssErr != nil || cpuErr != nil {
			continue
		}
		processes[pid] = processInfo{group: group, rssKB: rss, cpuSeconds: cpu}
	}
	return processes, nil
}

// parseCPUTime parses a ps time value such as "0:01.25", "12:03.50", or "1:02:03.00".
func parseCPUTime(value string) (float64, error) {
	if _, after, found := strings.Cut(value, "-"); found {
		// A days prefix ("2-03:04:05") does not occur for benchmark processes.
		value = after
	}
	total := 0.0
	for _, part := range strings.Split(value, ":") {
		number, err := strconv.ParseFloat(part, 64)
		if err != nil {
			return 0, err
		}
		total = total*60 + number
	}
	return total, nil
}

// sampleProcFS reads /proc/<pid>/stat for every process.
func sampleProcFS() (map[int]processInfo, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	pageKB := int64(os.Getpagesize() / 1024)
	processes := map[int]processInfo{}
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}
		content, err := os.ReadFile(filepath.Join("/proc", entry.Name(), "stat"))
		if err != nil {
			continue
		}
		if info, ok := parseProcStat(string(content), pageKB); ok {
			processes[pid] = info
		}
	}
	return processes, nil
}

// parseProcStat reads the process group (field 5), utime and stime (fields 14 and 15), and
// rss in pages (field 24) from a /proc/<pid>/stat line. The command name in field 2 may
// contain spaces, so fields are counted after its closing parenthesis.
func parseProcStat(content string, pageKB int64) (processInfo, bool) {
	closing := strings.LastIndex(content, ")")
	if closing < 0 {
		return processInfo{}, false
	}
	fields := strings.Fields(content[closing+1:])
	// fields[0] is field 3 (state), so field N is fields[N-3].
	if len(fields) < 22 {
		return processInfo{}, false
	}
	group, groupErr := strconv.Atoi(fields[2])
	userTicks, userErr := strconv.ParseInt(fields[11], 10, 64)
	systemTicks, systemErr := strconv.ParseInt(fields[12], 10, 64)
	rssPages, rssErr := strconv.ParseInt(fields[21], 10, 64)
	if groupErr != nil || userErr != nil || systemErr != nil || rssErr != nil {
		return processInfo{}, false
	}
	return processInfo{
		group:      group,
		rssKB:      rssPages * pageKB,
		cpuSeconds: float64(userTicks+systemTicks) / linuxClockTicks,
	}, true
}

// usageTracker accumulates CPU time and peak memory of the measured processes over a
// window. CPU time per process is its last observed value minus its value in the first
// sample, or minus zero for a process that started later. A process that exits counts up
// to its last sample.
type usageTracker struct {
	started      bool
	first        map[int]float64
	last         map[int]float64
	isDaemon     map[int]bool
	peakTotalKB  int64
	peakDaemonKB int64
	maxProcesses int
}

// newUsageTracker returns an empty tracker.
func newUsageTracker() *usageTracker {
	return &usageTracker{first: map[int]float64{}, last: map[int]float64{}, isDaemon: map[int]bool{}}
}

// record adds one sample: the processes in the daemon's process group and the shims.
func (t *usageTracker) record(processes map[int]processInfo, daemonGroup int, shimPIDs []int) {
	var totalKB, daemonKB int64
	count := 0
	add := func(pid int, info processInfo, daemon bool) {
		if _, seen := t.first[pid]; !seen {
			t.first[pid] = 0
			if !t.started {
				t.first[pid] = info.cpuSeconds
			}
		}
		t.last[pid] = info.cpuSeconds
		t.isDaemon[pid] = daemon
		totalKB += info.rssKB
		if daemon {
			daemonKB += info.rssKB
		}
		count++
	}
	for pid, info := range processes {
		if info.group == daemonGroup {
			add(pid, info, true)
		}
	}
	for _, pid := range shimPIDs {
		if info, ok := processes[pid]; ok && info.group != daemonGroup {
			add(pid, info, false)
		}
	}
	t.started = true
	t.peakTotalKB = max(t.peakTotalKB, totalKB)
	t.peakDaemonKB = max(t.peakDaemonKB, daemonKB)
	t.maxProcesses = max(t.maxProcesses, count)
}

// cpuSeconds returns the CPU time used in the window by the daemon group and by the shims.
func (t *usageTracker) cpuSeconds() (daemon float64, shims float64) {
	for pid, last := range t.last {
		used := last - t.first[pid]
		if t.isDaemon[pid] {
			daemon += used
		} else {
			shims += used
		}
	}
	return daemon, shims
}
