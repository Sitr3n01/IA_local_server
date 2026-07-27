package edge

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

const recentEventLimit = 100

type event struct {
	Time       string `json:"time"`
	RequestID  string `json:"request_id"`
	Method     string `json:"method"`
	Path       string `json:"path"`
	Status     int    `json:"status"`
	DurationMS int64  `json:"duration_ms"`
}

type eventStore struct {
	mu     sync.RWMutex
	events []event
	output io.Writer
}

func newEventStore(output io.Writer) *eventStore {
	if output == nil {
		output = io.Discard
	}
	return &eventStore{output: output}
}

func (s *eventStore) add(item event) {
	line, err := json.Marshal(item)
	if err == nil {
		line = append(line, '\n')
		_, _ = s.output.Write(line)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.events) == recentEventLimit {
		copy(s.events, s.events[1:])
		s.events[len(s.events)-1] = item
		return
	}
	s.events = append(s.events, item)
}

func (s *eventStore) recent() []event {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]event, len(s.events))
	copy(result, s.events)
	return result
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(status int) {
	if w.status != 0 {
		return
	}
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (w *statusWriter) Write(data []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(data)
}

func (w *statusWriter) Flush() {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (w *statusWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

func requestID() string {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return hex.EncodeToString([]byte(time.Now().UTC().Format("150405.000000000")))
	}
	return hex.EncodeToString(value[:])
}

func safeRoute(path string) string {
	if strings.HasPrefix(path, modelControlPrefix) {
		for _, operation := range []string{"load", "unload", "switch"} {
			if strings.HasSuffix(path, ":"+operation) {
				return modelControlPrefix + ":" + operation
			}
		}
	}
	switch path {
	case "/v1/models", "/v1/responses", "/v1/chat/completions", "/livez", "/readyz", "/metrics", "/api/v1/status":
		return path
	default:
		return "unknown"
	}
}

func safeMethod(method string) string {
	switch method {
	case http.MethodGet, http.MethodPost, http.MethodHead, http.MethodOptions:
		return method
	default:
		return "OTHER"
	}
}
