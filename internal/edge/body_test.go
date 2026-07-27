package edge

import (
	"bytes"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/klauspost/compress/zstd"
)

func TestDecodeRequestBodyLimits(t *testing.T) {
	tests := []struct {
		name       string
		body       []byte
		encoding   string
		maxWire    int64
		maxDecoded int64
		maxRatio   int64
		want       error
	}{
		{name: "wire limit", body: []byte("12345"), maxWire: 4, maxDecoded: 8, maxRatio: 100, want: errWireTooLarge},
		{name: "decoded limit", body: []byte("12345"), maxWire: 8, maxDecoded: 4, maxRatio: 100, want: errDecodedTooLarge},
		{name: "unknown encoding", body: []byte("{}"), encoding: "br", maxWire: 8, maxDecoded: 8, maxRatio: 100, want: errUnsupportedEncoding},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest("POST", "http://127.0.0.1/", bytes.NewReader(test.body))
			request.Header.Set("Content-Encoding", test.encoding)
			_, err := decodeRequestBody(request, test.maxWire, test.maxDecoded, test.maxRatio)
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestDecodeRequestBodyRejectsCompressionRatio(t *testing.T) {
	decoded := []byte(`{"model":"local-coding","input":"` + strings.Repeat("A", 32<<10) + `"}`)
	encoder, err := zstd.NewWriter(nil)
	if err != nil {
		t.Fatal(err)
	}
	compressed := encoder.EncodeAll(decoded, nil)
	encoder.Close()
	request := httptest.NewRequest("POST", "http://127.0.0.1/", bytes.NewReader(compressed))
	request.Header.Set("Content-Encoding", "zstd")
	_, err = decodeRequestBody(request, 1<<20, 1<<20, 10)
	if !errors.Is(err, errCompressionRatio) {
		t.Fatalf("error = %v, want compression ratio error; compressed=%d decoded=%d", err, len(compressed), len(decoded))
	}
}
