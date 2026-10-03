package edge

import (
	"errors"
	"io/fs"
	"os"
	"strings"
)

// ArtifactStatus says whether a model's weights file is where the manifest
// says it is. It answers from the file's metadata alone: the edge never opens a
// weights file, and the release transaction, not the edge, is what hashes it.
//
// Both fields are pointers because "could not tell" is a real third answer: a
// file the edge is not permitted to stat is not a missing file, and reporting it
// as missing would take a healthy model out of service.
type ArtifactStatus struct {
	// Present is false only when the file does not exist (or is a directory).
	Present *bool `json:"present"`
	// SizeMatches compares the file's size with the manifest's, which catches a
	// truncated download or a file replaced by another quantization.
	SizeMatches *bool `json:"size_matches"`
}

// Reasons the status route gives a model whose file is not what was deployed.
const (
	reasonArtifactMissing      = "artifact_missing"
	reasonArtifactSizeMismatch = "artifact_size_mismatch"
)

// checkArtifact is called for every model each time the status is read, which
// is a few stat calls; nothing is cached because a deletion should show on the
// next read.
func checkArtifact(path string, expectedBytes *int64) ArtifactStatus {
	path = strings.TrimSpace(path)
	if path == "" {
		return ArtifactStatus{}
	}
	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return ArtifactStatus{Present: boolValue(false)}
		}
		return ArtifactStatus{}
	}
	if info.IsDir() {
		return ArtifactStatus{Present: boolValue(false)}
	}
	status := ArtifactStatus{Present: boolValue(true)}
	if expectedBytes != nil && *expectedBytes > 0 {
		status.SizeMatches = boolValue(info.Size() == *expectedBytes)
	}
	return status
}

func boolValue(value bool) *bool { return &value }

// refuse reports the reason a model with this artifact status cannot be
// served, or "" when nothing is wrong or nothing is known.
func (a ArtifactStatus) refuse() string {
	switch {
	case a.Present != nil && !*a.Present:
		return reasonArtifactMissing
	case a.SizeMatches != nil && !*a.SizeMatches:
		return reasonArtifactSizeMismatch
	}
	return ""
}
