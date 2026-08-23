// Package adminpipe carries administrative mutations over a local transport
// that the operating system authenticates, instead of one authenticated by a
// bearer token the caller has to hold in memory.
//
// On Windows the transport is a named pipe whose DACL admits only SYSTEM, the
// built-in Administrators group, and the account the edge itself runs as. No
// credential travels over it: a process that cannot open the pipe cannot issue
// an operation, and a process that captures the channel captures nothing worth
// replaying. The wire format is deliberately tiny — one bounded, newline
// terminated JSON request and one response per connection — so the parser has
// no state machine to get wrong.
package adminpipe

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"unicode"
)

const (
	// MaxMessageBytes bounds one request. Administrative requests carry a verb
	// and a model ID; anything larger is malformed or hostile.
	MaxMessageBytes = 8 << 10
	// MaxModelIDBytes matches the administrative HTTP client's own bound so the
	// two transports refuse exactly the same identifiers.
	MaxModelIDBytes = 256
)

// Operations accepted on the administrative transport. Reads stay on the HTTP
// control plane; this channel exists for mutations only.
const (
	OperationLoad   = "load"
	OperationUnload = "unload"
	OperationSwitch = "switch"
	OperationDrain  = "maintenance.drain"
	OperationResume = "maintenance.resume"
)

// Request is one administrative command. Unknown fields are rejected: a client
// that sends something this server does not understand is not a client this
// server should guess for.
type Request struct {
	Operation string `json:"operation"`
	ModelID   string `json:"model_id,omitempty"`
}

// Result is the success payload. Its field names match the administrative HTTP
// response so a client can parse either transport with one type.
type Result struct {
	Operation   string          `json:"operation"`
	Model       string          `json:"model,omitempty"`
	Status      string          `json:"status"`
	ActiveModel string          `json:"active_model"`
	Maintenance json.RawMessage `json:"maintenance,omitempty"`
}

// Error is the failure payload. Messages are fixed strings chosen by the
// server; nothing derived from a prompt, a body, or a credential appears here.
type Error struct {
	Code       string `json:"code"`
	Message    string `json:"message"`
	HTTPStatus int    `json:"http_status,omitempty"`
}

func (e *Error) Error() string {
	if e == nil {
		return "administrative operation failed"
	}
	if e.Message == "" {
		return e.Code
	}
	return e.Code + ": " + e.Message
}

// Response is the single envelope written back for every request.
type Response struct {
	OK     bool    `json:"ok"`
	Result *Result `json:"result,omitempty"`
	Error  *Error  `json:"error,omitempty"`
}

// Handler executes one validated administrative request. Implementations own
// admission policy; this package owns only framing and validation.
type Handler interface {
	Execute(ctx context.Context, request Request) (Result, *Error)
}

var (
	errMessageTooLarge = errors.New("administrative request exceeds the message limit")
	// errPipeClosed normalises the several Windows errors that all mean "the
	// peer went away", so the framing code has one end-of-stream condition.
	errPipeClosed = errors.New("administrative pipe connection closed")
	// ErrUnsupported reports a platform without the administrative pipe
	// transport. The DACL is the authentication, so there is nothing to emulate.
	ErrUnsupported = errors.New("the administrative pipe transport is available on Windows only")
	// ErrNotListening reports that no process is serving the pipe at all. It is
	// the single condition under which a client may fall back to the deprecated
	// HTTP administrative plane: every other failure — a wrong server
	// executable, a refused open, a malformed reply — means something is there
	// that should not be, and falling back would hand it the very bearer token
	// this transport exists to stop putting on the wire.
	ErrNotListening = errors.New("no administrative pipe is listening")
)

// ServeConn reads exactly one request, executes it, and writes exactly one
// response. It never reads a second message from the same connection, so a
// client cannot pipeline commands behind a single authorization decision.
func ServeConn(ctx context.Context, conn io.ReadWriter, handler Handler) error {
	request, readErr := readRequest(conn)
	if readErr != nil {
		return writeResponse(conn, Response{OK: false, Error: readErr})
	}
	result, failure := handler.Execute(ctx, request)
	if failure != nil {
		return writeResponse(conn, Response{OK: false, Error: failure})
	}
	return writeResponse(conn, Response{OK: true, Result: &result})
}

func readRequest(conn io.Reader) (Request, *Error) {
	reader := bufio.NewReaderSize(io.LimitReader(conn, MaxMessageBytes+1), MaxMessageBytes+1)
	line, err := reader.ReadSlice('\n')
	switch {
	case errors.Is(err, bufio.ErrBufferFull), len(line) > MaxMessageBytes:
		return Request{}, &Error{Code: "message_too_large", Message: "administrative request exceeds 8 KiB", HTTPStatus: 413}
	case err != nil && !errors.Is(err, io.EOF):
		return Request{}, &Error{Code: "invalid_request", Message: "administrative request could not be read", HTTPStatus: 400}
	case len(line) == 0:
		return Request{}, &Error{Code: "invalid_request", Message: "administrative request was empty", HTTPStatus: 400}
	}

	decoder := json.NewDecoder(strings.NewReader(string(line)))
	decoder.DisallowUnknownFields()
	var request Request
	if err := decoder.Decode(&request); err != nil {
		return Request{}, &Error{Code: "invalid_request", Message: "administrative request must be a single JSON object", HTTPStatus: 400}
	}
	if decoder.More() {
		return Request{}, &Error{Code: "invalid_request", Message: "administrative request must contain exactly one JSON object", HTTPStatus: 400}
	}
	return validateRequest(request)
}

func validateRequest(request Request) (Request, *Error) {
	switch request.Operation {
	case OperationLoad, OperationUnload, OperationSwitch:
		modelID, err := ValidateModelID(request.ModelID)
		if err != nil {
			return Request{}, &Error{Code: "invalid_request", Message: err.Error(), HTTPStatus: 400}
		}
		request.ModelID = modelID
		return request, nil
	case OperationDrain, OperationResume:
		// Maintenance is provider-wide. Accepting a model ID here would imply a
		// per-model drain that does not exist, so it fails closed instead.
		if strings.TrimSpace(request.ModelID) != "" {
			return Request{}, &Error{Code: "invalid_request", Message: "maintenance operations do not accept a model_id", HTTPStatus: 400}
		}
		return request, nil
	default:
		return Request{}, &Error{Code: "unknown_operation", Message: "administrative operation is not supported", HTTPStatus: 404}
	}
}

// ValidateModelID applies the model identifier bounds shared by every
// administrative transport.
func ValidateModelID(modelID string) (string, error) {
	modelID = strings.TrimSpace(modelID)
	if modelID == "" {
		return "", errors.New("model_id is required")
	}
	if len(modelID) > MaxModelIDBytes {
		return "", errors.New("model_id exceeds the permitted length")
	}
	for _, r := range modelID {
		if unicode.IsControl(r) {
			return "", errors.New("model_id contains control characters")
		}
	}
	return modelID, nil
}

func writeResponse(conn io.Writer, response Response) error {
	payload, err := json.Marshal(response)
	if err != nil {
		return err
	}
	payload = append(payload, '\n')
	_, err = conn.Write(payload)
	return err
}
