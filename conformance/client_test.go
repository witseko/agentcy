//go:build conformance

package conformance

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"time"
)

const (
	// maxRequest and maxResponse are the protocol's line caps, newline included.
	maxRequest  = 128 * 1024
	maxResponse = 2 * 1024 * 1024
	// The daemon gives up on a silent client after 5 seconds; a waiting read lasts up to 45.
	responseTimeout = 60 * time.Second
)

// exchange sends one payload on a new connection and returns the reply line without its
// newline. It returns an empty string when the daemon closes the connection without a reply.
// With closeWrite the client half-closes the connection after the payload, so the daemon
// sees the end of the request even without a newline.
func exchange(socket string, payload []byte, closeWrite bool) (string, error) {
	connection, err := net.DialTimeout("unix", socket, 5*time.Second)
	if err != nil {
		return "", fmt.Errorf("connect: %w", err)
	}
	defer connection.Close()
	if err := connection.SetDeadline(time.Now().Add(responseTimeout)); err != nil {
		return "", err
	}
	// Write in the background: the daemon may answer an oversize line before reading all of it.
	go writePayload(connection.(*net.UnixConn), payload, closeWrite)
	return readReply(connection)
}

// writePayload writes the request. A write error means the daemon already answered and closed.
func writePayload(connection *net.UnixConn, payload []byte, closeWrite bool) {
	if _, err := connection.Write(payload); err != nil {
		return
	}
	if closeWrite {
		_ = connection.CloseWrite()
	}
}

// readReply reads one bounded line and rejects a reply that is too long or unterminated.
func readReply(connection net.Conn) (string, error) {
	reader := bufio.NewReader(io.LimitReader(connection, maxResponse+1))
	line, err := reader.ReadBytes('\n')
	if errors.Is(err, io.EOF) && len(line) == 0 {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("unterminated reply of %d bytes: %w", len(line), err)
	}
	if len(line) > maxResponse {
		return "", fmt.Errorf("reply exceeds %d bytes", maxResponse)
	}
	return string(bytes.TrimSuffix(line, []byte("\n"))), nil
}
