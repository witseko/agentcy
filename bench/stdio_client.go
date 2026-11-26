//go:build bench

package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"syscall"
	"time"
)

// mcpProtocolVersion is the revision the benchmark client asks for in initialize.
const mcpProtocolVersion = "2025-11-25"

// shimProcess is one shim started as `AGENTCY_BIN --workspace DIR`, spoken to with
// newline-delimited JSON-RPC over its standard input and output.
type shimProcess struct {
	session *session
	command *exec.Cmd
	stdin   io.WriteCloser
	stdout  *bufio.Reader
	nextID  int
	exited  chan struct{}
}

// rpcMessage is any JSON-RPC message the shim writes: a response, a request, or a notification.
type rpcMessage struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// startShim starts a shim for the benchmark workspace. Its standard error is discarded.
func startShim(s *session) (*shimProcess, error) {
	command := exec.Command(s.cfg.shimPath, "--workspace", s.workspace)
	command.Env = s.processEnvironment()
	stdin, err := command.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := command.Start(); err != nil {
		return nil, fmt.Errorf("start shim: %w", err)
	}
	shim := &shimProcess{
		session: s,
		command: command,
		stdin:   stdin,
		stdout:  bufio.NewReader(stdout),
		exited:  make(chan struct{}),
	}
	go func() {
		command.Wait()
		close(shim.exited)
	}()
	s.trackShim(shim, true)
	return shim, nil
}

// pid returns the shim's process ID.
func (p *shimProcess) pid() int {
	return p.command.Process.Pid
}

// send writes one JSON-RPC message as one line.
func (p *shimProcess) send(message map[string]any) error {
	message["jsonrpc"] = "2.0"
	line, err := json.Marshal(message)
	if err != nil {
		return err
	}
	_, err = p.stdin.Write(append(line, '\n'))
	return err
}

// call sends a request and reads lines until the response with the same ID arrives.
// It refuses requests from the shim, since the client declares no capabilities.
func (p *shimProcess) call(method string, params any) (json.RawMessage, error) {
	p.nextID++
	id := strconv.Itoa(p.nextID)
	if err := p.send(map[string]any{"id": p.nextID, "method": method, "params": params}); err != nil {
		return nil, err
	}
	for {
		line, err := p.stdout.ReadBytes('\n')
		if err != nil {
			return nil, fmt.Errorf("shim closed its output during %s: %w", method, err)
		}
		var message rpcMessage
		if err := json.Unmarshal(line, &message); err != nil {
			continue
		}
		if message.Method != "" && len(message.ID) > 0 {
			p.refuse(message.ID)
			continue
		}
		if string(message.ID) != id {
			continue
		}
		if message.Error != nil {
			return nil, fmt.Errorf("%s failed: %s", method, message.Error.Message)
		}
		return message.Result, nil
	}
}

// refuse answers a request from the shim with "method not found".
func (p *shimProcess) refuse(id json.RawMessage) {
	p.send(map[string]any{
		"id":    id,
		"error": map[string]any{"code": -32601, "message": "Method not found"},
	})
}

// initialize runs the MCP handshake: initialize, then notifications/initialized.
func (p *shimProcess) initialize() error {
	_, err := p.call("initialize", map[string]any{
		"protocolVersion": mcpProtocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "agentcy-bench", "version": "1"},
	})
	if err != nil {
		return err
	}
	return p.send(map[string]any{"method": "notifications/initialized"})
}

// toolResult holds the parts of a tools/call result the benchmark reads.
type toolResult struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	StructuredContent json.RawMessage `json:"structuredContent"`
	IsError           bool            `json:"isError"`
}

// callTool calls one tool and returns its result object, decoded from the first text
// content block, or from structuredContent when there is no text block.
func (p *shimProcess) callTool(name string, arguments map[string]any) (json.RawMessage, error) {
	raw, err := p.call("tools/call", map[string]any{"name": name, "arguments": arguments})
	if err != nil {
		return nil, err
	}
	var result toolResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, err
	}
	text := ""
	if len(result.Content) > 0 && result.Content[0].Type == "text" {
		text = result.Content[0].Text
	}
	if result.IsError {
		return nil, fmt.Errorf("tool %s returned an error: %s", name, text)
	}
	if text != "" && json.Valid([]byte(text)) {
		return json.RawMessage(text), nil
	}
	if len(result.StructuredContent) > 0 {
		return result.StructuredContent, nil
	}
	return nil, errors.New("tool " + name + " returned no JSON result")
}

// stop closes the shim's input, then terminates it if it has not exited shortly after.
func (p *shimProcess) stop() {
	p.stdin.Close()
	select {
	case <-p.exited:
	case <-time.After(500 * time.Millisecond):
		p.command.Process.Signal(syscall.SIGTERM)
		select {
		case <-p.exited:
		case <-time.After(processStopTimeout):
			p.command.Process.Kill()
			<-p.exited
		}
	}
	p.session.trackShim(p, false)
}
