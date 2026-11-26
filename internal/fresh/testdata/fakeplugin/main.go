// Command fakeplugin is a tiny fresh-run plugin for the client tests. The tests compile
// it from this directory. FAKE_MODE picks its behaviour, FAKE_LOG receives every line it
// reads, and FAKE_ENV_OUT receives its environment.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

func main() {
	mode := os.Getenv("FAKE_MODE")
	if path := os.Getenv("FAKE_ENV_OUT"); path != "" {
		_ = os.WriteFile(path, []byte(strings.Join(os.Environ(), "\n")), 0o600)
	}
	fmt.Fprintln(os.Stderr, "fake plugin diagnostics that must be discarded")
	switch mode {
	case "exit":
		os.Exit(3)
	case "grandchild":
		child := exec.Command("/bin/sleep", "60")
		_ = child.Start()
		if path := os.Getenv("FAKE_PID_OUT"); path != "" {
			_ = os.WriteFile(path, []byte(fmt.Sprint(os.Getpid(), " ", child.Process.Pid)), 0o600)
		}
		time.Sleep(time.Hour)
	case "oversize":
		fmt.Println(strings.Repeat("x", 2<<20))
		time.Sleep(time.Hour)
	case "malformed":
		fmt.Println("this is not json")
		time.Sleep(time.Hour)
	}
	var logFile *os.File
	if path := os.Getenv("FAKE_LOG"); path != "" {
		logFile, _ = os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	}
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		line := scanner.Text()
		if logFile != nil {
			fmt.Fprintln(logFile, line)
		}
		var message map[string]any
		if err := json.Unmarshal([]byte(line), &message); err != nil {
			os.Exit(4)
		}
		id, hasID := message["id"]
		if !hasID {
			continue
		}
		switch message["method"] {
		case "initialize":
			version := "2025-11-25"
			if mode == "bad-version" {
				version = "2099-01-01"
			}
			if mode == "notify-first" {
				reply(map[string]any{"jsonrpc": "2.0", "method": "notifications/message", "params": map[string]any{"data": "x"}})
			}
			reply(map[string]any{"jsonrpc": "2.0", "id": id, "result": map[string]any{
				"protocolVersion": version, "capabilities": map[string]any{}, "serverInfo": map[string]any{"name": "fake"},
			}})
		case "tools/call":
			params, _ := message["params"].(map[string]any)
			answer(mode, id, params["name"])
			if mode == "exit-after-reply" {
				os.Exit(5)
			}
		}
	}
	if mode == "linger" {
		time.Sleep(time.Hour)
	}
}

// answer replies to the one tools/call according to the mode.
func answer(mode string, id, tool any) {
	switch mode {
	case "slow":
		time.Sleep(time.Hour)
	case "tool-error":
		reply(map[string]any{"jsonrpc": "2.0", "id": id, "result": map[string]any{"content": []any{}, "isError": true}})
	case "rpc-error":
		reply(map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": -32601, "message": "secret-reply-text"}})
	case "no-structured":
		reply(map[string]any{"jsonrpc": "2.0", "id": id, "result": map[string]any{"content": []any{}}})
	case "bad-state":
		reply(map[string]any{"jsonrpc": "2.0", "id": id, "result": map[string]any{"structuredContent": map[string]any{"state": "secret-reply-text"}}})
	default:
		content := map[string]any{"state": "running", "exit_code": nil}
		if tool == "capabilities" {
			content = map[string]any{"version": 1, "token_cap": mode == "token-cap"}
		}
		reply(map[string]any{"jsonrpc": "2.0", "id": id, "result": map[string]any{
			"content": []any{}, "isError": false, "structuredContent": content,
		}})
	}
}

func reply(message map[string]any) {
	encoded, _ := json.Marshal(message)
	fmt.Println(string(encoded))
}
