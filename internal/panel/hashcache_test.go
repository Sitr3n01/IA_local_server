package panel

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestHashCacheReusesAndInvalidatesRecords(t *testing.T) {
	root := t.TempDir()
	model := filepath.Join(root, "model.gguf")
	if err := os.WriteFile(model, []byte("GGUF-one"), 0o600); err != nil {
		t.Fatal(err)
	}
	cache, err := NewHashCache(filepath.Join(root, "hashes.json"))
	if err != nil {
		t.Fatal(err)
	}
	first, cached, err := cache.HashFile(model)
	if err != nil || cached {
		t.Fatalf("first hash = %+v, cached=%v, err=%v", first, cached, err)
	}
	second, cached, err := cache.HashFile(model)
	if err != nil || !cached || second.SHA256 != first.SHA256 {
		t.Fatalf("second hash = %+v, cached=%v, err=%v", second, cached, err)
	}
	if err := os.WriteFile(model, []byte("GGUF-two"), 0o600); err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(time.Second)
	if err := os.Chtimes(model, future, future); err != nil {
		t.Fatal(err)
	}
	third, cached, err := cache.HashFile(model)
	if err != nil || cached || third.SHA256 == first.SHA256 {
		t.Fatalf("invalidated hash = %+v, cached=%v, err=%v", third, cached, err)
	}
}
