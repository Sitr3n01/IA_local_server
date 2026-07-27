package rotatelog

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWriterRotatesAndBoundsBackups(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "service.jsonl")
	w, err := Open(path, 8, 2, 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"first\n", "second\n", "third\n", "fourth\n"} {
		if _, err := w.Write([]byte(value)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path + ".3"); !os.IsNotExist(err) {
		t.Fatalf("unexpected third backup: %v", err)
	}
	current, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(current), "fourth") {
		t.Fatalf("current log = %q", current)
	}
}

func TestWriterRejectsInvalidPolicy(t *testing.T) {
	t.Parallel()
	if _, err := Open("", 1, 1, time.Hour); err == nil {
		t.Fatal("Open accepted an empty path")
	}
}
