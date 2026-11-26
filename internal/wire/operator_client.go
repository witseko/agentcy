package wire

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"
)

// Operator client timeouts.
const (
	defaultOperatorDial    = 2 * time.Second
	defaultOperatorTimeout = 10 * time.Second
)

// ErrCoreUnreachable marks a failed dial or missing operator key file, for example after
// the core exited while idle.
var ErrCoreUnreachable = errors.New("core is not reachable")

// operatorResponse is one line from the core: a result or an error.
type operatorResponse struct {
	Result json.RawMessage `json:"result"`
	Error  *string         `json:"error"`
}

// OperatorClient sends operator protocol requests to the core over its Unix socket with
// one request per connection and automatic operator.key reload on key rejection.
type OperatorClient struct {
	Home        string
	Timeout     time.Duration
	DialTimeout time.Duration
	mutex       sync.Mutex
	key         string
}

// NewOperatorClient returns an operator client for the bridge data directory home.
func NewOperatorClient(home string) *OperatorClient {
	return &OperatorClient{
		Home:        home,
		Timeout:     defaultOperatorTimeout,
		DialTimeout: defaultOperatorDial,
	}
}

// Call sends one operator request and decodes its JSON result into result. After a key
// rejection it re-reads operator.key once and retries, because the core writes a new key
// at every start.
func (client *OperatorClient) Call(ctx context.Context, action string, request any, result any) error {
	key, err := client.operatorKey(false)
	if err != nil {
		return err
	}
	err = client.exchange(ctx, action, key, request, result)
	var coreErr *CoreError
	if !errors.As(err, &coreErr) || coreErr.Message != OperatorKeyRejected {
		return err
	}
	key, err = client.operatorKey(true)
	if err != nil {
		return err
	}
	return client.exchange(ctx, action, key, request, result)
}

// operatorKey returns the cached key, reading operator.key when asked or when uncached.
func (client *OperatorClient) operatorKey(reload bool) (string, error) {
	client.mutex.Lock()
	defer client.mutex.Unlock()
	if client.key != "" && !reload {
		return client.key, nil
	}
	file, err := os.Open(filepath.Join(client.Home, OperatorKeyFileName))
	if err != nil {
		return "", fmt.Errorf("%w: operator key file is missing", ErrCoreUnreachable)
	}
	defer file.Close()
	content, err := io.ReadAll(io.LimitReader(file, MaxOperatorKeyBytes+1))
	if err != nil || len(content) > MaxOperatorKeyBytes {
		return "", errors.New("operator key file is unreadable")
	}
	key := string(bytes.TrimSpace(content))
	if key == "" {
		return "", errors.New("operator key file is empty")
	}
	client.key = key
	return key, nil
}

// EncodeOperatorRequest merges the operator envelope (version, key, action) into the
// marshaled JSON object of request in a single pass, ending with a newline.
func EncodeOperatorRequest(action, key string, request any) ([]byte, error) {
	var body []byte
	if request != nil {
		var err error
		body, err = json.Marshal(request)
		if err != nil {
			return nil, err
		}
	} else {
		body = []byte("{}")
	}
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) < 2 || trimmed[0] != '{' || trimmed[len(trimmed)-1] != '}' {
		return nil, errors.New("operator request must marshal to a JSON object")
	}
	inner := bytes.TrimSpace(trimmed[1 : len(trimmed)-1])
	var buf bytes.Buffer
	buf.Grow(len(inner) + len(action) + len(key) + 48)
	buf.WriteString(`{"version":`)
	buf.WriteString(strconv.Itoa(OperatorProtocolVersion))
	buf.WriteString(`,"key":`)
	buf.WriteString(strconv.Quote(key))
	buf.WriteString(`,"action":`)
	buf.WriteString(strconv.Quote(action))
	if len(inner) > 0 {
		buf.WriteByte(',')
		buf.Write(inner)
	}
	buf.WriteString("}\n")
	return buf.Bytes(), nil
}

// DecodeOperatorResponse maps an error line to *CoreError and unmarshals a result line
// into result.
func DecodeOperatorResponse(reply []byte, result any) error {
	var decoded operatorResponse
	if err := json.Unmarshal(reply, &decoded); err != nil {
		return errors.New("malformed response from the core")
	}
	if decoded.Error != nil {
		return &CoreError{Message: *decoded.Error}
	}
	if decoded.Result == nil {
		return errors.New("malformed response from the core")
	}
	if err := json.Unmarshal(decoded.Result, result); err != nil {
		return errors.New("unexpected result from the core")
	}
	return nil
}

// exchange writes one request line and reads one bounded response line within Timeout.
func (client *OperatorClient) exchange(ctx context.Context, action, key string, request any, result any) error {
	line, err := EncodeOperatorRequest(action, key, request)
	if err != nil {
		return err
	}
	dialTimeout := client.DialTimeout
	if dialTimeout <= 0 {
		dialTimeout = defaultOperatorDial
	}
	dialer := net.Dialer{Timeout: dialTimeout}
	connection, err := dialer.DialContext(ctx, "unix", filepath.Join(client.Home, SocketFileName))
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return ErrCoreUnreachable
	}
	defer connection.Close()
	stopCancel := context.AfterFunc(ctx, func() {
		_ = connection.Close()
	})
	defer stopCancel()
	timeout := client.Timeout
	if timeout <= 0 {
		timeout = defaultOperatorTimeout
	}
	if err := connection.SetDeadline(time.Now().Add(timeout)); err != nil {
		return err
	}
	if err := WriteLine(connection, line); err != nil {
		return fmt.Errorf("sending the request failed: %w", err)
	}
	reply, err := ReadLine(connection, MaxResponseBytes)
	if err != nil {
		return fmt.Errorf("incomplete response from the core: %w", err)
	}
	return DecodeOperatorResponse(reply, result)
}
