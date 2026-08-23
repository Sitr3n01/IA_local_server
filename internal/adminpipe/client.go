package adminpipe

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

const (
	defaultDialTimeout = 5 * time.Second
	maxDialTimeout     = 5 * time.Minute
)

// DialOptions describes how a client reaches the administrative pipe.
type DialOptions struct {
	// Name is the full pipe path, for example \\.\pipe\cia-local-ai-admin-final.
	Name string
	// Timeout bounds both the connection attempt and the exchange.
	Timeout time.Duration
	// ExpectedServerPath is the executable that must own the pipe. It defends
	// against a name squatter: whoever creates a pipe name first owns it, so a
	// client that skips this check can be answered by an impostor. No credential
	// is ever sent, so a squatter learns nothing, but it could still fake a
	// success the operator would believe.
	ExpectedServerPath string
}

func (o DialOptions) normalized() (DialOptions, error) {
	if strings.TrimSpace(o.Name) == "" {
		return DialOptions{}, errors.New("administrative pipe name is required")
	}
	if strings.TrimSpace(o.ExpectedServerPath) == "" {
		return DialOptions{}, errors.New("administrative pipe server executable is required")
	}
	if o.Timeout == 0 {
		o.Timeout = defaultDialTimeout
	}
	if o.Timeout < 0 || o.Timeout > maxDialTimeout {
		return DialOptions{}, fmt.Errorf("administrative pipe timeout must be greater than zero and at most %s", maxDialTimeout)
	}
	return o, nil
}

// Client performs one administrative operation per connection over the
// DACL-protected pipe. It holds no credential, because the transport does not
// use one.
type Client struct {
	options DialOptions
}

// NewClient validates the dial policy up front so a misconfigured client fails
// before it can reach any endpoint.
func NewClient(options DialOptions) (*Client, error) {
	normalized, err := options.normalized()
	if err != nil {
		return nil, err
	}
	return &Client{options: normalized}, nil
}

// Name reports the pipe this client targets.
func (c *Client) Name() string { return c.options.Name }

// Execute sends one request and returns the server's single response. A
// protocol-level failure is returned as *Error so callers can distinguish a
// refusal from a transport problem.
func (c *Client) Execute(ctx context.Context, request Request) (Result, error) {
	if _, err := validateRequest(request); err != nil {
		return Result{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, c.options.Timeout)
	defer cancel()

	conn, err := Dial(ctx, c.options)
	if err != nil {
		return Result{}, err
	}
	defer conn.Close()

	return exchange(conn, request)
}

func exchange(conn io.ReadWriter, request Request) (Result, error) {
	payload, err := json.Marshal(request)
	if err != nil {
		return Result{}, err
	}
	payload = append(payload, '\n')
	if _, err := conn.Write(payload); err != nil {
		return Result{}, fmt.Errorf("send administrative request: %w", err)
	}

	raw, readErr := readBoundedLine(conn)
	if readErr != nil {
		return Result{}, readErr
	}
	var response Response
	if err := json.Unmarshal(raw, &response); err != nil {
		return Result{}, errors.New("administrative transport returned invalid JSON")
	}
	if !response.OK {
		if response.Error == nil {
			return Result{}, errors.New("administrative transport reported an unspecified failure")
		}
		return Result{}, response.Error
	}
	if response.Result == nil {
		return Result{}, errors.New("administrative transport returned an empty result")
	}
	return *response.Result, nil
}

func readBoundedLine(conn io.Reader) ([]byte, error) {
	buffer := make([]byte, 0, 512)
	chunk := make([]byte, 512)
	for {
		read, err := conn.Read(chunk)
		if read > 0 {
			buffer = append(buffer, chunk[:read]...)
			if index := indexNewline(buffer); index >= 0 {
				return buffer[:index], nil
			}
			if len(buffer) > MaxMessageBytes {
				return nil, errMessageTooLarge
			}
		}
		if err != nil {
			if len(buffer) > 0 && (errors.Is(err, io.EOF) || errors.Is(err, errPipeClosed)) {
				return buffer, nil
			}
			if errors.Is(err, io.EOF) || errors.Is(err, errPipeClosed) {
				return nil, errors.New("administrative transport closed without a response")
			}
			return nil, fmt.Errorf("read administrative response: %w", err)
		}
	}
}

func indexNewline(data []byte) int {
	for index, value := range data {
		if value == '\n' {
			return index
		}
	}
	return -1
}
