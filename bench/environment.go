//go:build bench

package main

import (
	"bufio"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// environment is the environment block of the report.
type environment struct {
	OS          string `json:"os"`
	Kernel      string `json:"kernel"`
	CPUModel    string `json:"cpu_model"`
	Cores       int    `json:"cores"`
	GoVersion   string `json:"go_version"`
	CoreCommand string `json:"core_command"`
	ShimPath    string `json:"shim_path"`
	Messages    int    `json:"messages"`
	FillMethod  string `json:"fill_method"`
	Date        string `json:"date"`
}

// describeEnvironment collects the machine and run details for the report.
func describeEnvironment(cfg config, fillMethod string) environment {
	return environment{
		OS:          runtime.GOOS + "/" + runtime.GOARCH,
		Kernel:      commandOutput("uname", "-sr"),
		CPUModel:    cpuModel(),
		Cores:       runtime.NumCPU(),
		GoVersion:   runtime.Version(),
		CoreCommand: strings.Join(cfg.coreCommand, " "),
		ShimPath:    cfg.shimPath,
		Messages:    cfg.messages,
		FillMethod:  fillMethod,
		Date:        time.Now().Format(time.RFC3339),
	}
}

// cpuModel returns the CPU model name, or "unknown".
func cpuModel() string {
	if runtime.GOOS == "darwin" {
		return commandOutput("sysctl", "-n", "machdep.cpu.brand_string")
	}
	file, err := os.Open("/proc/cpuinfo")
	if err != nil {
		return "unknown"
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		name, value, found := strings.Cut(scanner.Text(), ":")
		if found && strings.TrimSpace(name) == "model name" {
			return strings.TrimSpace(value)
		}
	}
	return "unknown"
}

// commandOutput returns the trimmed output of a command, or "unknown" when it fails.
func commandOutput(program string, arguments ...string) string {
	output, err := exec.Command(program, arguments...).Output()
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(string(output))
}
