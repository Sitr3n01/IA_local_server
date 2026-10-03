package adminpipe

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// recordingHandler answers every well-formed request and records what reached
// it, so a test can prove the framing layer rejected what it should have.
type recordingHandler struct {
	seen    []Request
	failure *Error
}

func (h *recordingHandler) Execute(_ context.Context, request Request) (Result, *Error) {
	h.seen = append(h.seen, request)
	if h.failure != nil {
		return Result{}, h.failure
	}
	return Result{
		Operation:   request.Operation,
		Model:       request.ModelID,
		Status:      "completed",
		ActiveModel: request.ModelID,
		Maintenance: json.RawMessage(`{"state":"maintenance","drained":true}`),
	}, nil
}

// duplexBuffer is a one-shot connection stand-in: it reads the request the test
// supplies and captures whatever the server writes back.
type duplexBuffer struct {
	in  *strings.Reader
	out bytes.Buffer
}

func (d *duplexBuffer) Read(p []byte) (int, error)  { return d.in.Read(p) }
func (d *duplexBuffer) Write(p []byte) (int, error) { return d.out.Write(p) }

func serve(t *testing.T, handler Handler, request string) Response {
	t.Helper()
	connection := &duplexBuffer{in: strings.NewReader(request)}
	if err := ServeConn(context.Background(), connection, handler); err != nil {
		t.Fatalf("ServeConn: %v", err)
	}
	var response Response
	if err := json.Unmarshal(bytes.TrimSpace(connection.out.Bytes()), &response); err != nil {
		t.Fatalf("decode response %q: %v", connection.out.String(), err)
	}
	return response
}

func TestServeConnAcceptsExactlyTheSupportedOperations(t *testing.T) {
	for _, operation := range []string{OperationLoad, OperationUnload, OperationSwitch} {
		handler := &recordingHandler{}
		response := serve(t, handler, `{"operation":"`+operation+`","model_id":"local-coding"}`+"\n")
		if !response.OK || response.Result == nil {
			t.Fatalf("%s was refused: %+v", operation, response.Error)
		}
		if response.Result.Operation != operation || response.Result.Model != "local-coding" {
			t.Fatalf("%s produced %+v", operation, response.Result)
		}
	}

	for _, operation := range []string{OperationDrain, OperationResume} {
		handler := &recordingHandler{}
		response := serve(t, handler, `{"operation":"`+operation+`"}`+"\n")
		if !response.OK || response.Result == nil {
			t.Fatalf("%s was refused: %+v", operation, response.Error)
		}
		if len(handler.seen) != 1 || handler.seen[0].ModelID != "" {
			t.Fatalf("%s reached the handler with a model: %+v", operation, handler.seen)
		}
	}
}

func TestServeConnRefusesMalformedOversizedAndUnknownRequests(t *testing.T) {
	cases := map[string]struct {
		request string
		code    string
	}{
		"unknown operation":         {`{"operation":"restart"}` + "\n", "unknown_operation"},
		"empty operation":           {`{"operation":""}` + "\n", "unknown_operation"},
		"missing model id":          {`{"operation":"load"}` + "\n", "invalid_request"},
		"blank model id":            {`{"operation":"load","model_id":"   "}` + "\n", "invalid_request"},
		"control characters":        {`{"operation":"load","model_id":"a\u0000b"}` + "\n", "invalid_request"},
		"model id on maintenance":   {`{"operation":"maintenance.drain","model_id":"local-coding"}` + "\n", "invalid_request"},
		"unknown field":             {`{"operation":"load","model_id":"m","force":true}` + "\n", "invalid_request"},
		"not an object":             {`"load"` + "\n", "invalid_request"},
		"truncated json":            {`{"operation":` + "\n", "invalid_request"},
		"two objects on one line":   {`{"operation":"load","model_id":"m"} {"operation":"unload","model_id":"m"}` + "\n", "invalid_request"},
		"trailing brace":            {`{"operation":"load","model_id":"m"}}` + "\n", "invalid_request"},
		"trailing bracket":          {`{"operation":"load","model_id":"m"}]` + "\n", "invalid_request"},
		"empty line":                {"\n", "invalid_request"},
		"oversized without newline": {strings.Repeat("a", MaxMessageBytes+64), "message_too_large"},
		"oversized model id":        {`{"operation":"load","model_id":"` + strings.Repeat("m", MaxModelIDBytes+1) + `"}` + "\n", "invalid_request"},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			handler := &recordingHandler{}
			response := serve(t, handler, testCase.request)
			if response.OK {
				t.Fatalf("%s was accepted", name)
			}
			if response.Error == nil || response.Error.Code != testCase.code {
				t.Fatalf("%s error = %+v, want code %q", name, response.Error, testCase.code)
			}
			if len(handler.seen) != 0 {
				t.Fatalf("%s reached the handler: %+v", name, handler.seen)
			}
		})
	}
}

func TestServeConnReadsOnlyOneRequestPerConnection(t *testing.T) {
	handler := &recordingHandler{}
	serve(t, handler, `{"operation":"load","model_id":"first"}`+"\n"+`{"operation":"unload","model_id":"second"}`+"\n")
	if len(handler.seen) != 1 || handler.seen[0].ModelID != "first" {
		t.Fatalf("pipelined commands were executed: %+v", handler.seen)
	}
}

func TestServeConnPropagatesHandlerRefusalsWithoutInventingDetail(t *testing.T) {
	handler := &recordingHandler{failure: &Error{Code: "inference_busy", Message: "model control is unavailable while inference is active or queued", HTTPStatus: 409}}
	response := serve(t, handler, `{"operation":"unload","model_id":"local-coding"}`+"\n")
	if response.OK || response.Error == nil {
		t.Fatal("handler refusal was reported as success")
	}
	if response.Error.Code != "inference_busy" || response.Error.HTTPStatus != 409 {
		t.Fatalf("refusal was rewritten: %+v", response.Error)
	}
}

func TestClientRequiresAnExpectedServerExecutable(t *testing.T) {
	if _, err := NewClient(DialOptions{Name: `\\.\pipe\cia-local-ai-admin-final`}); err == nil {
		t.Fatal("a client without an expected server executable was accepted")
	}
	if _, err := NewClient(DialOptions{Name: "", ExpectedServerPath: `C:\IA\local-ai-v2\bin\cia-edge.exe`}); err == nil {
		t.Fatal("a client without a pipe name was accepted")
	}
}

func TestExchangeSurfacesServerErrorsAsProtocolErrors(t *testing.T) {
	connection := &duplexBuffer{in: strings.NewReader(`{"ok":false,"error":{"code":"model_not_found","message":"requested model is not available"}}` + "\n")}

	_, err := exchange(connection, Request{Operation: OperationLoad, ModelID: "absent"})
	var protocolError *Error
	if !errors.As(err, &protocolError) {
		t.Fatalf("exchange error = %v, want *Error", err)
	}
	if protocolError.Code != "model_not_found" {
		t.Fatalf("unexpected error code: %+v", protocolError)
	}
	if !strings.Contains(connection.out.String(), `"operation":"load"`) {
		t.Fatalf("client did not send the request: %s", connection.out.String())
	}
}

// A credential must never appear on this transport. The check is structural:
// the request type has exactly two fields and neither is a token.
func TestRequestCarriesNoCredentialFields(t *testing.T) {
	encoded, err := json.Marshal(Request{Operation: OperationLoad, ModelID: "local-coding"})
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded) != 2 {
		t.Fatalf("request carries unexpected fields: %v", decoded)
	}
	for _, forbidden := range []string{"token", "authorization", "bearer", "api_key", "password"} {
		if strings.Contains(strings.ToLower(string(encoded)), forbidden) {
			t.Fatalf("request wire format mentions %q: %s", forbidden, encoded)
		}
	}
}
