// Package modeloverlay persists non-manifest local model inspection state.
// It deliberately lives under the installed runtime state directory, never in
// the source checkout or versioned manifest.
package modeloverlay

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

const (
	schemaVersion = 1
	maxStateBytes = 4 << 20
)

type Capabilities struct {
	Responses        bool `json:"responses"`
	ChatCompletions  bool `json:"chat_completions"`
	Streaming        bool `json:"streaming"`
	FunctionCalling  bool `json:"function_calling"`
	StructuredOutput bool `json:"structured_output"`
	Reasoning        bool `json:"reasoning"`
}

// Profile records an immutable inspected artifact identity plus its measured
// runtime result. A detected/inspected artifact is never eligible for publish.
type Profile struct {
	ID           string       `json:"id"`
	DisplayName  string       `json:"display_name"`
	Origin       string       `json:"origin"`
	State        string       `json:"state"`
	ArtifactPath string       `json:"artifact_path"`
	SHA256       string       `json:"sha256"`
	Bytes        int64        `json:"bytes"`
	GGUFVersion  uint32       `json:"gguf_version"`
	Runtime      string       `json:"runtime,omitempty"`
	Capabilities Capabilities `json:"capabilities"`
	Validation   string       `json:"validation"`
	Message      string       `json:"message,omitempty"`
}

func (p Profile) Eligible() bool {
	return p.Origin == "local" && p.State == "validated" && p.Validation == "passed" && p.ArtifactPath != "" && len(p.SHA256) == 64 && p.Capabilities.ChatCompletions && p.Capabilities.Streaming
}

type document struct {
	SchemaVersion int       `json:"schema_version"`
	Profiles      []Profile `json:"profiles"`
}

type Store struct {
	path string
	mu   sync.Mutex
}

func New(path string) (*Store, error) {
	if strings.TrimSpace(path) != path || !filepath.IsAbs(path) {
		return nil, errors.New("overlay path must be absolute")
	}
	return &Store{path: path}, nil
}

func (s *Store) Load() ([]Profile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	document, err := s.loadLocked()
	if err != nil {
		return nil, err
	}
	return append([]Profile(nil), document.Profiles...), nil
}

// Upsert persists one profile, deduplicating a physical artifact by canonical
// path and SHA. A retired profile ID is never reused: callers must create a
// new local identity when the same file is discovered again.
func (s *Store) Upsert(profile Profile) ([]Profile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := validateProfile(profile); err != nil {
		return nil, err
	}
	document, err := s.loadLocked()
	if err != nil {
		return nil, err
	}
	for index := range document.Profiles {
		current := &document.Profiles[index]
		if current.ID == profile.ID {
			if current.State == "retired" && profile.State != "retired" {
				return nil, errors.New("a retired overlay profile ID cannot be revived")
			}
			*current = profile
			return s.saveLocked(document)
		}
		if strings.EqualFold(current.ArtifactPath, profile.ArtifactPath) && strings.EqualFold(current.SHA256, profile.SHA256) && current.State != "retired" {
			*current = profile
			return s.saveLocked(document)
		}
	}
	document.Profiles = append(document.Profiles, profile)
	return s.saveLocked(document)
}

func (s *Store) loadLocked() (document, error) {
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return document{SchemaVersion: schemaVersion}, nil
	}
	if err != nil {
		return document{}, err
	}
	if len(data) > maxStateBytes {
		return document{}, errors.New("model overlay exceeds maximum size")
	}
	var result document
	decoder := json.NewDecoder(bytes.NewReader(bytes.TrimPrefix(data, []byte{0xef, 0xbb, 0xbf})))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result); err != nil {
		return document{}, err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return document{}, errors.New("model overlay must contain one JSON value")
	}
	if result.SchemaVersion != schemaVersion {
		return document{}, errors.New("unsupported model overlay schema")
	}
	for _, profile := range result.Profiles {
		if err := validateProfile(profile); err != nil {
			return document{}, err
		}
	}
	return result, nil
}

func (s *Store) saveLocked(document document) ([]Profile, error) {
	sort.Slice(document.Profiles, func(i, j int) bool { return document.Profiles[i].ID < document.Profiles[j].ID })
	data, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := writeAtomic(s.path, append(data, '\n')); err != nil {
		return nil, err
	}
	return append([]Profile(nil), document.Profiles...), nil
}

// Inspect confirms that a candidate is a regular GGUF under an approved,
// non-reparse root, hashes it, and creates a conservative inspected record.
// It intentionally makes no inference request and publishes no capability.
func Inspect(root, path string) (Profile, error) {
	canonicalRoot, err := canonicalRoot(root)
	if err != nil {
		return Profile{}, err
	}
	canonicalPath, err := canonicalFile(canonicalRoot, path)
	if err != nil {
		return Profile{}, err
	}
	file, err := os.Open(canonicalPath)
	if err != nil {
		return Profile{}, err
	}
	defer file.Close()
	header := make([]byte, 8)
	if _, err := io.ReadFull(file, header); err != nil {
		return Profile{}, fmt.Errorf("read GGUF header: %w", err)
	}
	if string(header[:4]) != "GGUF" {
		return Profile{}, errors.New("candidate does not have a GGUF header")
	}
	version := binary.LittleEndian.Uint32(header[4:])
	if version < 2 || version > 3 {
		return Profile{}, fmt.Errorf("unsupported GGUF version %d", version)
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return Profile{}, err
	}
	hash := sha256.New()
	bytes, err := io.Copy(hash, file)
	if err != nil {
		return Profile{}, err
	}
	digest := hex.EncodeToString(hash.Sum(nil))
	idDigest := sha256.Sum256([]byte(strings.ToLower(canonicalPath) + "\n" + digest))
	return Profile{
		ID:           "local-" + hex.EncodeToString(idDigest[:6]),
		DisplayName:  filepath.Base(canonicalPath),
		Origin:       "local",
		State:        "inspected",
		ArtifactPath: canonicalPath,
		SHA256:       digest,
		Bytes:        bytes,
		GGUFVersion:  version,
		Validation:   "inspected",
		Message:      "GGUF header and SHA-256 verified; runtime validation is still required",
	}, nil
}

func validateProfile(profile Profile) error {
	if !strings.HasPrefix(profile.ID, "local-") || len(profile.ID) < 8 || strings.TrimSpace(profile.DisplayName) == "" || profile.Origin != "local" {
		return errors.New("invalid overlay profile identity")
	}
	if profile.State != "detected" && profile.State != "inspected" && profile.State != "validated" && profile.State != "failed" && profile.State != "retired" {
		return errors.New("invalid overlay profile state")
	}
	if profile.Validation != "detected" && profile.Validation != "inspected" && profile.Validation != "passed" && profile.Validation != "failed" {
		return errors.New("invalid overlay validation state")
	}
	if !filepath.IsAbs(profile.ArtifactPath) || profile.Bytes <= 0 || len(profile.SHA256) != 64 {
		return errors.New("overlay profile artifact is incomplete")
	}
	if _, err := hex.DecodeString(profile.SHA256); err != nil {
		return errors.New("overlay profile SHA-256 is invalid")
	}
	return nil
}

func canonicalRoot(root string) (string, error) {
	if !filepath.IsAbs(root) {
		return "", errors.New("approved model root must be absolute")
	}
	info, err := os.Lstat(root)
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return "", errors.New("approved model root must be a non-reparse directory")
	}
	return filepath.Clean(root), nil
}

func canonicalFile(root, path string) (string, error) {
	if !filepath.IsAbs(path) || !strings.EqualFold(filepath.Ext(path), ".gguf") {
		return "", errors.New("candidate must be an absolute GGUF path")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return "", errors.New("candidate must be a regular non-reparse file")
	}
	canonical := filepath.Clean(path)
	relative, err := filepath.Rel(root, canonical)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", errors.New("candidate is outside the approved model root")
	}
	return canonical, nil
}

func writeAtomic(path string, contents []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".model-overlay-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(contents); err != nil {
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
	return os.Rename(temporaryPath, path)
}
