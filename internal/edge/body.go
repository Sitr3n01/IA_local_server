package edge

import (
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/klauspost/compress/zstd"
)

var (
	errWireTooLarge        = errors.New("compressed request body exceeds the wire limit")
	errDecodedTooLarge     = errors.New("decoded request body exceeds the decoded limit")
	errCompressionRatio    = errors.New("request body exceeds the maximum compression ratio")
	errUnsupportedEncoding = errors.New("unsupported content encoding")
)

func decodeRequestBody(r *http.Request, maxWire, maxDecoded, maxRatio int64) ([]byte, error) {
	if r.ContentLength > maxWire {
		return nil, errWireTooLarge
	}

	wire, err := io.ReadAll(io.LimitReader(r.Body, maxWire+1))
	if err != nil {
		return nil, fmt.Errorf("read request body: %w", err)
	}
	if int64(len(wire)) > maxWire {
		return nil, errWireTooLarge
	}

	encoding := strings.ToLower(strings.TrimSpace(r.Header.Get("Content-Encoding")))
	var reader io.Reader
	var closeReader func()
	switch encoding {
	case "", "identity":
		if int64(len(wire)) > maxDecoded {
			return nil, errDecodedTooLarge
		}
		return wire, nil
	case "gzip":
		gzipReader, gzipErr := gzip.NewReader(bytes.NewReader(wire))
		err = gzipErr
		if err != nil {
			return nil, fmt.Errorf("decode gzip request: %w", err)
		}
		reader = gzipReader
		closeReader = func() { _ = gzipReader.Close() }
	case "zstd":
		zstdReader, zstdErr := zstd.NewReader(
			bytes.NewReader(wire),
			zstd.WithDecoderMaxMemory(uint64(maxDecoded*2)),
			zstd.WithDecoderMaxWindow(uint64(maxDecoded)),
		)
		err = zstdErr
		if err != nil {
			return nil, fmt.Errorf("decode zstd request: %w", err)
		}
		reader = zstdReader
		closeReader = zstdReader.Close
	default:
		return nil, errUnsupportedEncoding
	}
	defer closeReader()

	decoded, err := io.ReadAll(io.LimitReader(reader, maxDecoded+1))
	if err != nil {
		return nil, fmt.Errorf("decode request body: %w", err)
	}
	if int64(len(decoded)) > maxDecoded {
		return nil, errDecodedTooLarge
	}
	if len(wire) == 0 {
		if len(decoded) != 0 {
			return nil, errCompressionRatio
		}
		return decoded, nil
	}
	if int64(len(decoded)) > int64(len(wire))*maxRatio {
		return nil, errCompressionRatio
	}
	return decoded, nil
}
