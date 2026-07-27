package panel

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSelectionMissingFallsBackWithoutWriting(t *testing.T) {
	catalog := testCatalog(t)
	path := filepath.Join(t.TempDir(), "selection.json")
	store, err := NewSelectionStore(path, catalog)
	if err != nil {
		t.Fatal(err)
	}
	selection, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if selection.Model != catalog.PublicModel {
		t.Fatalf("fallback = %q, want %q", selection.Model, catalog.PublicModel)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("fallback unexpectedly wrote state: %v", err)
	}
}

func TestSelectionSaveIsAtomicAndReloadable(t *testing.T) {
	catalog := testCatalog(t)
	path := filepath.Join(t.TempDir(), "state", "selection.json")
	store, err := NewSelectionStore(path, catalog)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Save("local-coding"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Save("local-coding"); err != nil {
		t.Fatalf("atomic replacement of existing state failed: %v", err)
	}
	selection, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if selection.SchemaVersion != 1 || selection.Model != "local-coding" {
		t.Fatalf("unexpected selection: %+v", selection)
	}
	temporaries, err := filepath.Glob(filepath.Join(filepath.Dir(path), ".selection-*.tmp"))
	if err != nil || len(temporaries) != 0 {
		t.Fatalf("temporary files remain: %v, %v", temporaries, err)
	}
}

func TestSelectionRejectsUnavailableWithoutChangingState(t *testing.T) {
	catalog := testCatalog(t)
	path := filepath.Join(t.TempDir(), "selection.json")
	store, err := NewSelectionStore(path, catalog)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Save("local-coding"); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Save("local-fast"); err == nil {
		t.Fatal("unavailable selection was saved")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatal("rejected selection changed durable state")
	}
}

func TestSelectionRejectsCorruptionWithoutRewriting(t *testing.T) {
	catalog := testCatalog(t)
	path := writeTestFile(t, "selection.json", "{\"schema_version\":1,\"model\":\"local-coding\",\"unexpected\":true}")
	store, err := NewSelectionStore(path, catalog)
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(); err == nil {
		t.Fatal("corrupt selection was accepted")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatal("corrupt selection was rewritten")
	}
}
