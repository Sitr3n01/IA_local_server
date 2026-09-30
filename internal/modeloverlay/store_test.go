package modeloverlay

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

func testGGUF(t *testing.T, root, name string) string {
	t.Helper()
	path := filepath.Join(root, name)
	header := make([]byte, 16)
	copy(header, "GGUF")
	binary.LittleEndian.PutUint32(header[4:8], 3)
	if err := os.WriteFile(path, header, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestInspectAndStoreRoundTrip(t *testing.T) {
	root := t.TempDir()
	profile, err := Inspect(root, testGGUF(t, root, "future-model.gguf"))
	if err != nil {
		t.Fatal(err)
	}
	if profile.State != "inspected" || profile.Validation != "inspected" || profile.Eligible() {
		t.Fatalf("unexpected inspected profile: %+v", profile)
	}
	store, err := New(filepath.Join(t.TempDir(), "state", "model-overlay.json"))
	if err != nil {
		t.Fatal(err)
	}
	profiles, err := store.Upsert(profile)
	if err != nil || len(profiles) != 1 {
		t.Fatalf("Upsert: profiles=%+v err=%v", profiles, err)
	}
	profile.State, profile.Validation = "validated", "passed"
	profile.Capabilities.ChatCompletions, profile.Capabilities.Streaming = true, true
	profiles, err = store.Upsert(profile)
	if err != nil || len(profiles) != 1 || !profiles[0].Eligible() {
		t.Fatalf("validated Upsert: profiles=%+v err=%v", profiles, err)
	}
	loaded, err := store.Load()
	if err != nil || len(loaded) != 1 || loaded[0].SHA256 != profile.SHA256 {
		t.Fatalf("Load: profiles=%+v err=%v", loaded, err)
	}
}

func TestRetiredProfileNeverRevives(t *testing.T) {
	root := t.TempDir()
	profile, err := Inspect(root, testGGUF(t, root, "retired.gguf"))
	if err != nil {
		t.Fatal(err)
	}
	store, _ := New(filepath.Join(t.TempDir(), "model-overlay.json"))
	profile.State, profile.Validation = "retired", "failed"
	if _, err := store.Upsert(profile); err != nil {
		t.Fatal(err)
	}
	profile.State, profile.Validation = "inspected", "inspected"
	if _, err := store.Upsert(profile); err == nil {
		t.Fatal("retired profile was revived")
	}
}

func TestInspectRejectsOutsideRootAndInvalidHeader(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.gguf")
	if err := os.WriteFile(outside, []byte("GGUF"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Inspect(root, outside); err == nil {
		t.Fatal("outside candidate passed")
	}
	invalid := filepath.Join(root, "invalid.gguf")
	if err := os.WriteFile(invalid, []byte("not-a-gguf"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Inspect(root, invalid); err == nil {
		t.Fatal("invalid header passed")
	}
}
