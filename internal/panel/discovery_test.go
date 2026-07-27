package panel

import (
	"os"
	"path/filepath"
	"testing"
)

func TestModelRootStoreDiscoversGGUFAndDeduplicatesRoots(t *testing.T) {
	root := t.TempDir()
	state := filepath.Join(t.TempDir(), "roots.json")
	if err := os.WriteFile(filepath.Join(root, "model.gguf"), []byte("gguf"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "ignore.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := NewModelRootStore(state, root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Add(root); err != nil {
		t.Fatal(err)
	}
	models, err := store.Scan()
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 1 || models[0].Name != "model.gguf" || models[0].Bytes != 4 {
		t.Fatalf("models = %+v", models)
	}
}

func TestModelRootStoreNeverRemovesCanonicalRoot(t *testing.T) {
	root := t.TempDir()
	store, err := NewModelRootStore(filepath.Join(t.TempDir(), "roots.json"), root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Remove(root); err == nil {
		t.Fatal("canonical root was removable")
	}
}
