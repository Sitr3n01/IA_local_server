package panel

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const validationSchemaVersion = 1

type ValidationRecord struct {
	Status       string `json:"status"`
	UpdatedAt    string `json:"updated_at"`
	Message      string `json:"message,omitempty"`
	ArtifactPath string `json:"artifact_path,omitempty"`
	SHA256       string `json:"sha256,omitempty"`
}

type validationDocument struct {
	SchemaVersion int                         `json:"schema_version"`
	Models        map[string]ValidationRecord `json:"models"`
}

type ValidationStore struct {
	path string
	mu   sync.Mutex
}

func NewValidationStore(path string) (*ValidationStore, error) {
	if err := validateAbsolutePath("validation path", path); err != nil {
		return nil, err
	}
	return &ValidationStore{path: path}, nil
}

func (s *ValidationStore) Load() (map[string]ValidationRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	document, err := s.loadLocked()
	if err != nil {
		return nil, err
	}
	result := make(map[string]ValidationRecord, len(document.Models))
	for id, record := range document.Models {
		result[id] = record
	}
	return result, nil
}

func (s *ValidationStore) Record(modelID, status, message string) error {
	return s.RecordArtifact(modelID, status, message, "", "")
}

func (s *ValidationStore) RecordArtifact(modelID, status, message, artifactPath, sha256 string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	document, err := s.loadLocked()
	if err != nil {
		return err
	}
	if document.Models == nil {
		document.Models = make(map[string]ValidationRecord)
	}
	if len(message) > 300 {
		message = message[:300]
	}
	document.Models[modelID] = ValidationRecord{
		Status: status, UpdatedAt: time.Now().UTC().Format(time.RFC3339), Message: strings.TrimSpace(message),
		ArtifactPath: strings.TrimSpace(artifactPath), SHA256: strings.ToUpper(strings.TrimSpace(sha256)),
	}
	data, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	directory := filepath.Dir(s.path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(directory, ".validation-*.tmp")
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
	return os.Rename(temporaryPath, s.path)
}

func (s *ValidationStore) loadLocked() (validationDocument, error) {
	data, err := readLimitedFile(s.path, maxSelectionBytes)
	if errors.Is(err, os.ErrNotExist) {
		return validationDocument{SchemaVersion: validationSchemaVersion, Models: map[string]ValidationRecord{}}, nil
	}
	if err != nil {
		return validationDocument{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var document validationDocument
	if err := decoder.Decode(&document); err != nil {
		return validationDocument{}, err
	}
	if err := requireJSONEOF(decoder); err != nil {
		return validationDocument{}, err
	}
	if document.SchemaVersion != validationSchemaVersion {
		return validationDocument{}, errors.New("validation schema version is unsupported")
	}
	return document, nil
}
