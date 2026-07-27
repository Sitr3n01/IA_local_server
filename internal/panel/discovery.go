package panel

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	modelRootsSchemaVersion = 1
	maxModelRootsBytes      = 64 << 10
)

// DiscoveredModel is a GGUF file found in an explicitly approved root.
type DiscoveredModel struct {
	Path  string
	Name  string
	Bytes int64
	Root  string
}

type modelRootsDocument struct {
	SchemaVersion int      `json:"schema_version"`
	Roots         []string `json:"roots"`
}

// ModelRootStore persists the canonical GGUF search roots. It never deletes
// model files; removing a root only removes it from future inventory scans.
type ModelRootStore struct {
	path        string
	defaultRoot string
}

func NewModelRootStore(path, defaultRoot string) (*ModelRootStore, error) {
	if err := validateAbsolutePath("model roots path", path); err != nil {
		return nil, err
	}
	root, err := canonicalDirectory(defaultRoot)
	if err != nil {
		return nil, fmt.Errorf("default model root: %w", err)
	}
	return &ModelRootStore{path: path, defaultRoot: root}, nil
}

func (s *ModelRootStore) Load() ([]string, error) {
	data, err := readLimitedFile(s.path, maxModelRootsBytes)
	if errors.Is(err, os.ErrNotExist) {
		return []string{s.defaultRoot}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("open model roots: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var document modelRootsDocument
	if err := decoder.Decode(&document); err != nil {
		return nil, fmt.Errorf("decode model roots: %w", err)
	}
	if err := requireJSONEOF(decoder); err != nil {
		return nil, fmt.Errorf("decode model roots: %w", err)
	}
	if document.SchemaVersion != modelRootsSchemaVersion {
		return nil, fmt.Errorf("model roots schema_version must be %d", modelRootsSchemaVersion)
	}
	return s.normalize(document.Roots)
}

func (s *ModelRootStore) Add(path string) ([]string, error) {
	roots, err := s.Load()
	if err != nil {
		return nil, err
	}
	root, err := canonicalDirectory(path)
	if err != nil {
		return nil, err
	}
	for _, current := range roots {
		if strings.EqualFold(current, root) {
			return roots, nil
		}
	}
	return s.save(append(roots, root))
}

func (s *ModelRootStore) Remove(path string) ([]string, error) {
	roots, err := s.Load()
	if err != nil {
		return nil, err
	}
	root, err := canonicalDirectory(path)
	if err != nil {
		return nil, err
	}
	if strings.EqualFold(root, s.defaultRoot) {
		return nil, errors.New("the canonical C:\\IA\\models root cannot be removed")
	}
	filtered := make([]string, 0, len(roots))
	for _, current := range roots {
		if !strings.EqualFold(current, root) {
			filtered = append(filtered, current)
		}
	}
	return s.save(filtered)
}

func (s *ModelRootStore) Scan() ([]DiscoveredModel, error) {
	roots, err := s.Load()
	if err != nil {
		return nil, err
	}
	seen := make(map[string]struct{})
	models := make([]DiscoveredModel, 0)
	for _, root := range roots {
		err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() {
				return nil
			}
			if entry.Type()&os.ModeSymlink != 0 || !strings.EqualFold(filepath.Ext(entry.Name()), ".gguf") {
				return nil
			}
			canonical, err := filepath.Abs(path)
			if err != nil || !pathWithin(root, canonical) {
				return nil
			}
			key := strings.ToLower(filepath.Clean(canonical))
			if _, duplicate := seen[key]; duplicate {
				return nil
			}
			info, err := entry.Info()
			if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 {
				return nil
			}
			seen[key] = struct{}{}
			models = append(models, DiscoveredModel{Path: canonical, Name: entry.Name(), Bytes: info.Size(), Root: root})
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("scan model root %s: %w", root, err)
		}
	}
	sort.Slice(models, func(i, j int) bool { return strings.ToLower(models[i].Path) < strings.ToLower(models[j].Path) })
	return models, nil
}

func (s *ModelRootStore) normalize(raw []string) ([]string, error) {
	result := []string{s.defaultRoot}
	for _, value := range raw {
		root, err := canonicalDirectory(value)
		if err != nil {
			return nil, err
		}
		duplicate := false
		for _, current := range result {
			if strings.EqualFold(current, root) {
				duplicate = true
				break
			}
		}
		if !duplicate {
			result = append(result, root)
		}
	}
	return result, nil
}

func (s *ModelRootStore) save(roots []string) ([]string, error) {
	normalized, err := s.normalize(roots)
	if err != nil {
		return nil, err
	}
	document := modelRootsDocument{SchemaVersion: modelRootsSchemaVersion, Roots: normalized}
	data, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return nil, err
	}
	data = append(data, '\n')
	directory := filepath.Dir(s.path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return nil, err
	}
	temporary, err := os.CreateTemp(directory, ".model-roots-*.tmp")
	if err != nil {
		return nil, err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return nil, err
	}
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return nil, err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return nil, err
	}
	if err := temporary.Close(); err != nil {
		return nil, err
	}
	if err := os.Rename(temporaryPath, s.path); err != nil {
		return nil, err
	}
	return normalized, nil
}

func canonicalDirectory(path string) (string, error) {
	if err := validateAbsolutePath("model root", strings.TrimSpace(path)); err != nil {
		return "", err
	}
	absolute, err := filepath.Abs(strings.TrimSpace(path))
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", fmt.Errorf("model root is unavailable: %w", err)
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.IsDir() {
		return "", errors.New("model root must be an existing directory")
	}
	return filepath.Clean(resolved), nil
}

func pathWithin(root, path string) bool {
	relative, err := filepath.Rel(root, path)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}
