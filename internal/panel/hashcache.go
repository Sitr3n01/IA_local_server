package panel

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const (
	hashCacheSchemaVersion = 1
	maxHashCacheBytes      = 4 << 20
)

type HashRecord struct {
	Path             string `json:"path"`
	Bytes            int64  `json:"bytes"`
	ModifiedUnixNano int64  `json:"modified_unix_nano"`
	SHA256           string `json:"sha256"`
}

type hashCacheDocument struct {
	SchemaVersion int                   `json:"schema_version"`
	Entries       map[string]HashRecord `json:"entries"`
}

// HashCache avoids re-reading large immutable GGUFs while invalidating a
// record whenever its resolved path, size, or nanosecond modification time
// changes. The cache is evidence only; the manifest hash remains authoritative.
type HashCache struct {
	path string
	mu   sync.Mutex
}

func NewHashCache(path string) (*HashCache, error) {
	if err := validateAbsolutePath("hash cache path", path); err != nil {
		return nil, err
	}
	return &HashCache{path: path}, nil
}

func (c *HashCache) HashFile(path string) (HashRecord, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	resolved, info, err := regularResolvedFile(path)
	if err != nil {
		return HashRecord{}, false, err
	}
	key := strings.ToLower(filepath.Clean(resolved))
	document, err := c.loadLocked()
	if err != nil {
		return HashRecord{}, false, err
	}
	if record, ok := document.Entries[key]; ok &&
		record.Bytes == info.Size() && record.ModifiedUnixNano == info.ModTime().UnixNano() &&
		strings.EqualFold(record.Path, resolved) && len(record.SHA256) == 64 {
		return record, true, nil
	}

	file, err := os.Open(resolved)
	if err != nil {
		return HashRecord{}, false, fmt.Errorf("open model for hashing: %w", err)
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return HashRecord{}, false, fmt.Errorf("hash model: %w", err)
	}
	record := HashRecord{
		Path: resolved, Bytes: info.Size(), ModifiedUnixNano: info.ModTime().UnixNano(),
		SHA256: strings.ToUpper(hex.EncodeToString(hash.Sum(nil))),
	}
	if document.Entries == nil {
		document.Entries = make(map[string]HashRecord)
	}
	document.Entries[key] = record
	if err := c.saveLocked(document); err != nil {
		return HashRecord{}, false, err
	}
	return record, false, nil
}

func (c *HashCache) loadLocked() (hashCacheDocument, error) {
	data, err := readLimitedFile(c.path, maxHashCacheBytes)
	if errors.Is(err, os.ErrNotExist) {
		return hashCacheDocument{SchemaVersion: hashCacheSchemaVersion, Entries: map[string]HashRecord{}}, nil
	}
	if err != nil {
		return hashCacheDocument{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var document hashCacheDocument
	if err := decoder.Decode(&document); err != nil {
		return hashCacheDocument{}, fmt.Errorf("decode hash cache: %w", err)
	}
	if err := requireJSONEOF(decoder); err != nil {
		return hashCacheDocument{}, fmt.Errorf("decode hash cache: %w", err)
	}
	if document.SchemaVersion != hashCacheSchemaVersion {
		return hashCacheDocument{}, errors.New("hash cache schema version is unsupported")
	}
	if document.Entries == nil {
		document.Entries = make(map[string]HashRecord)
	}
	return document, nil
}

func (c *HashCache) saveLocked(document hashCacheDocument) error {
	document.SchemaVersion = hashCacheSchemaVersion
	data, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	directory := filepath.Dir(c.path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(directory, ".hash-cache-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, c.path)
}

func regularResolvedFile(path string) (string, os.FileInfo, error) {
	if err := validateAbsolutePath("model path", strings.TrimSpace(path)); err != nil {
		return "", nil, err
	}
	resolved, err := filepath.EvalSymlinks(filepath.Clean(path))
	if err != nil {
		return "", nil, fmt.Errorf("resolve model path: %w", err)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", nil, fmt.Errorf("stat model path: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() <= 0 {
		return "", nil, errors.New("model path must be a non-empty regular file")
	}
	return filepath.Clean(resolved), info, nil
}
