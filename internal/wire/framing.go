package wire

import (
	"bufio"
	"errors"
	"io"
)

// Shared file names and size limits for the bridge Unix socket protocol.
const (
	SocketFileName      = "core.sock"
	OperatorKeyFileName = "operator.key"
	MaxOperatorKeyBytes = 1024
)

// ErrLineTooLong is returned by ReadLine when a line exceeds the byte cap or ends without
// a terminating newline.
var ErrLineTooLong = errors.New("line exceeds size limit")

// ReadLine reads one newline-terminated line of at most limit bytes (including the
// newline). It returns io.EOF when the stream ends before any byte is read, and
// ErrLineTooLong when the line exceeds limit or ends without a newline.
func ReadLine(reader io.Reader, limit int) ([]byte, error) {
	buffered := bufio.NewReader(reader)
	var line []byte
	for len(line) <= limit {
		chunk, err := buffered.ReadSlice('\n')
		line = append(line, chunk...)
		if err == nil {
			break
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if errors.Is(err, io.EOF) {
			if len(line) == 0 {
				return nil, io.EOF
			}
			return nil, ErrLineTooLong
		}
		return nil, err
	}
	if len(line) > limit || line[len(line)-1] != '\n' {
		return nil, ErrLineTooLong
	}
	return line, nil
}

// WriteLine writes one newline-terminated JSON line to writer.
func WriteLine(writer io.Writer, payload []byte) error {
	if len(payload) == 0 || payload[len(payload)-1] != '\n' {
		payload = append(payload, '\n')
	}
	_, err := writer.Write(payload)
	return err
}
