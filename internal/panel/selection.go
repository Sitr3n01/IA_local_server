package panel

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	selectionSchemaVersion = 1
	maxSelectionBytes      = 16 << 10
)

// Selection is the only durable user preference owned by the panel. It never
// represents the model currently resident in memory; runtime truth comes from
// the control API.
type Selection struct {
	SchemaVersion int    `json:"schema_version"`
	Model         string `json:"model"`
}

// SelectionStore persists a selection atomically beside the configured state
// path. A missing file falls back in memory to provider.public_model; corrupt
// or unavailable selections fail closed and are never rewritten implicitly.
type SelectionStore struct {
	path    string
	catalog *Catalog
}

func NewSelectionStore(path string, catalog *Catalog) (*SelectionStore, error) {
	if err := validateAbsolutePath("selection path", path); err != nil {
		return nil, err
	}
	if catalog == nil || len(catalog.models) == 0 {
		return nil, errors.New("selection store requires a non-empty catalog")
	}
	return &SelectionStore{path: path, catalog: catalog}, nil
}

// Load reads a strict selection document. Only absence selects the public
// fallback; malformed or stale state is surfaced to the operator.
func (s *SelectionStore) Load() (Selection, error) {
	data, err := readLimitedFile(s.path, maxSelectionBytes)
	if errors.Is(err, os.ErrNotExist) {
		return s.fallback()
	}
	if err != nil {
		return Selection{}, fmt.Errorf("open panel selection: %w", err)
	}

	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var selection Selection
	if err := decoder.Decode(&selection); err != nil {
		return Selection{}, fmt.Errorf("decode panel selection: %w", err)
	}
	if err := requireJSONEOF(decoder); err != nil {
		return Selection{}, fmt.Errorf("decode panel selection: %w", err)
	}
	if err := s.validate(selection); err != nil {
		return Selection{}, err
	}
	return selection, nil
}

// Save validates before creating any file, then flushes a temporary file and
// atomically renames it over the configured state path.
func (s *SelectionStore) Save(modelID string) (Selection, error) {
	selection := Selection{SchemaVersion: selectionSchemaVersion, Model: modelID}
	if err := s.validate(selection); err != nil {
		return Selection{}, err
	}
	data, err := json.MarshalIndent(selection, "", "  ")
	if err != nil {
		return Selection{}, fmt.Errorf("encode panel selection: %w", err)
	}
	data = append(data, '\n')

	directory := filepath.Dir(s.path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return Selection{}, fmt.Errorf("create panel state directory: %w", err)
	}
	temporary, err := os.CreateTemp(directory, ".selection-*.tmp")
	if err != nil {
		return Selection{}, fmt.Errorf("create temporary panel selection: %w", err)
	}
	temporaryPath := temporary.Name()
	keepTemporary := false
	defer func() {
		_ = temporary.Close()
		if !keepTemporary {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return Selection{}, fmt.Errorf("protect temporary panel selection: %w", err)
	}
	if _, err := temporary.Write(data); err != nil {
		return Selection{}, fmt.Errorf("write temporary panel selection: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		return Selection{}, fmt.Errorf("flush temporary panel selection: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return Selection{}, fmt.Errorf("close temporary panel selection: %w", err)
	}
	if err := os.Rename(temporaryPath, s.path); err != nil {
		return Selection{}, fmt.Errorf("replace panel selection atomically: %w", err)
	}
	keepTemporary = true
	return selection, nil
}

func (s *SelectionStore) fallback() (Selection, error) {
	selection := Selection{SchemaVersion: selectionSchemaVersion, Model: s.catalog.PublicModel}
	if err := s.validate(selection); err != nil {
		return Selection{}, fmt.Errorf("public model fallback is unavailable: %w", err)
	}
	return selection, nil
}

func (s *SelectionStore) validate(selection Selection) error {
	if selection.SchemaVersion != selectionSchemaVersion {
		return fmt.Errorf("panel selection schema_version must be %d", selectionSchemaVersion)
	}
	if selection.Model == "" || strings.TrimSpace(selection.Model) != selection.Model {
		return errors.New("panel selection model is invalid")
	}
	model, found := s.catalog.Model(selection.Model)
	if !found {
		return fmt.Errorf("selected model %q is not in the catalog", selection.Model)
	}
	if !model.Available {
		return fmt.Errorf("selected model %q is unavailable: %s", selection.Model, model.UnavailableReason)
	}
	return nil
}
