package edge

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
)

// maxReleaseManifestBytes bounds the generated release manifest. It is written
// by the deployment transaction, so anything larger is a corrupted or
// substituted file rather than a legitimate release.
const maxReleaseManifestBytes = 1 << 20

// ReleaseInfo is the sanitized identity of the installed release. It is
// deliberately a strict subset of release.json: the file also records absolute
// paths and per-component hashes, and none of those belong on a status
// response. Nothing here is or derives from a credential.
type ReleaseInfo struct {
	Environment     string `json:"environment"`
	Release         string `json:"release"`
	Version         string `json:"version,omitempty"`
	Commit          string `json:"commit,omitempty"`
	SourceDirty     bool   `json:"source_dirty"`
	PreviousRelease string `json:"previous_release,omitempty"`
	Status          string `json:"status"`
	CreatedUTC      string `json:"created_utc,omitempty"`
}

// releaseManifest is the on-disk shape. Only the fields below are read; the
// deployment transaction writes more, and ignoring the rest here keeps the
// status contract from silently growing when the transaction record does.
type releaseManifest struct {
	SchemaVersion   int    `json:"schema_version"`
	Environment     string `json:"environment"`
	ReleaseID       string `json:"release_id"`
	Version         string `json:"version"`
	Commit          string `json:"commit"`
	SourceDirty     bool   `json:"source_dirty"`
	PreviousRelease string `json:"previous_release_id"`
	Status          string `json:"status"`
	CreatedUTC      string `json:"created_utc"`
}

var releaseStatuses = map[string]struct{}{
	"installed":   {},
	"rolled-back": {},
	"degraded":    {},
}

// LoadRelease reads and validates the installed release manifest. A missing
// path is not an error at the call site: the caller decides whether release
// metadata is required. A present but invalid manifest always fails, because a
// generated file that does not parse means the installation is not the one the
// deployment transaction certified.
func LoadRelease(path, environment string) (*ReleaseInfo, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("read release manifest: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("release manifest must be a regular file")
	}
	if info.Size() > maxReleaseManifestBytes {
		return nil, fmt.Errorf("release manifest exceeds %d bytes", maxReleaseManifestBytes)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read release manifest: %w", err)
	}

	// Unknown fields are ignored on purpose: release.json also records
	// component hashes and absolute paths that the edge must never surface.
	var manifest releaseManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return nil, errors.New("release manifest is not valid JSON")
	}
	if manifest.SchemaVersion != 1 {
		return nil, fmt.Errorf("unsupported release manifest schema_version %d", manifest.SchemaVersion)
	}
	if manifest.Environment != "canary" && manifest.Environment != "final" {
		return nil, errors.New("release manifest environment must be canary or final")
	}
	if environment != "" && manifest.Environment != environment {
		return nil, fmt.Errorf("release manifest environment %q does not match %q", manifest.Environment, environment)
	}
	if !validReleaseIdentifier(manifest.ReleaseID) {
		return nil, errors.New("release manifest release_id is missing or invalid")
	}
	if manifest.PreviousRelease != "" && !validReleaseIdentifier(manifest.PreviousRelease) {
		return nil, errors.New("release manifest previous_release_id is invalid")
	}
	if manifest.Version != "" && !validReleaseIdentifier(manifest.Version) {
		return nil, errors.New("release manifest version is invalid")
	}
	if manifest.Commit != "" && !validCommit(manifest.Commit) {
		return nil, errors.New("release manifest commit must be a full 40-character hexadecimal revision")
	}
	if _, ok := releaseStatuses[manifest.Status]; !ok {
		return nil, fmt.Errorf("release manifest status %q is not recognised", manifest.Status)
	}

	return &ReleaseInfo{
		Environment:     manifest.Environment,
		Release:         manifest.ReleaseID,
		Version:         manifest.Version,
		Commit:          manifest.Commit,
		SourceDirty:     manifest.SourceDirty,
		PreviousRelease: manifest.PreviousRelease,
		Status:          manifest.Status,
		CreatedUTC:      manifest.CreatedUTC,
	}, nil
}

func validReleaseIdentifier(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
		default:
			return false
		}
	}
	return true
}

func validCommit(value string) bool {
	if len(value) != 40 {
		return false
	}
	for _, r := range value {
		if !(r >= '0' && r <= '9') && !(r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}
