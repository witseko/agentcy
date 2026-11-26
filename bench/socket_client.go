//go:build bench

package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	// maxResponse matches the 2 MiB response cap of the socket protocol.
	maxResponse    = 2 * 1024 * 1024
	requestTimeout = 60 * time.Second
)

// socketResponse is one response line: a result or an error message.
type socketResponse struct {
	Result json.RawMessage `json:"result"`
	Error  *string         `json:"error"`
}

// exchange sends one request as one JSON line on a new connection and returns the result.
// A daemon error becomes a Go error with the daemon's message.
func exchange(socketPath string, request map[string]any) (json.RawMessage, error) {
	connection, err := net.DialTimeout("unix", socketPath, 5*time.Second)
	if err != nil {
		return nil, err
	}
	defer connection.Close()
	connection.SetDeadline(time.Now().Add(requestTimeout))

	line, err := json.Marshal(request)
	if err != nil {
		return nil, err
	}
	if _, err := connection.Write(append(line, '\n')); err != nil {
		return nil, err
	}
	reader := bufio.NewReader(io.LimitReader(connection, maxResponse+1))
	responseLine, err := reader.ReadBytes('\n')
	if err != nil {
		return nil, fmt.Errorf("incomplete response: %w", err)
	}
	var response socketResponse
	if err := json.Unmarshal(responseLine, &response); err != nil {
		return nil, err
	}
	if response.Error != nil {
		return nil, fmt.Errorf("daemon error for %v: %s", request["action"], *response.Error)
	}
	return response.Result, nil
}

// memberRequest builds a member protocol request for the benchmark workspace.
func (s *session) memberRequest(action string, fields map[string]any) map[string]any {
	request := map[string]any{"action": action, "workspace": s.workspaceKey}
	for name, value := range fields {
		request[name] = value
	}
	return request
}

// operatorClient sends operator protocol requests with the key the daemon wrote at start.
// The key never leaves this struct: it is not printed or logged.
type operatorClient struct {
	socketPath string
	directory  string
	key        string
}

// newOperatorClient reads operator.key from the temporary home.
func newOperatorClient(s *session) (*operatorClient, error) {
	content, err := os.ReadFile(filepath.Join(s.home, "operator.key"))
	if err != nil {
		return nil, errors.New("cannot read the operator key")
	}
	return &operatorClient{
		socketPath: s.socketPath(),
		directory:  s.workspace,
		key:        strings.TrimSpace(string(content)),
	}, nil
}

// call sends one operator action with the version, key, and checkout directory filled in.
func (c *operatorClient) call(action string, fields map[string]any) (json.RawMessage, error) {
	request := map[string]any{
		"version":   1,
		"key":       c.key,
		"action":    action,
		"directory": c.directory,
	}
	for name, value := range fields {
		request[name] = value
	}
	return exchange(c.socketPath, request)
}

// showTail returns the latest page of a conversation and its cursor (the last message ID).
func (c *operatorClient) showTail(conversation string) (showResult, error) {
	var result showResult
	raw, err := c.call("operator_show", map[string]any{"conversation": conversation, "tail": true})
	if err != nil {
		return result, err
	}
	err = json.Unmarshal(raw, &result)
	return result, err
}

// showResult holds the parts of an operator_show result the benchmark checks.
type showResult struct {
	Cursor   int64          `json:"cursor"`
	Messages []messageEntry `json:"messages"`
}

// messageEntry holds the parts of a transcript message the benchmark reads.
type messageEntry struct {
	ID   int64  `json:"id"`
	Body string `json:"body"`
}
