package panel

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestValidationStorePersistsArtifactEvidence(t *testing.T) {
	store, err := NewValidationStore(filepath.Join(t.TempDir(), "validation.json"))
	if err != nil {
		t.Fatal(err)
	}
	hash := strings.Repeat("a", 64)
	if err := store.RecordArtifact("detected-1", "inspecionado", "GGUF v3", `C:\models\one.gguf`, hash); err != nil {
		t.Fatal(err)
	}
	records, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	record := records["detected-1"]
	if record.Status != "inspecionado" || record.SHA256 != strings.ToUpper(hash) || record.ArtifactPath == "" {
		t.Fatalf("record = %+v", record)
	}
}
