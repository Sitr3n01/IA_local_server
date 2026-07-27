package main

import (
	"slices"
	"testing"
)

func TestRandomToken(t *testing.T) {
	t.Parallel()
	a, err := randomToken()
	if err != nil {
		t.Fatal(err)
	}
	b, err := randomToken()
	if err != nil {
		t.Fatal(err)
	}
	if len(a) < 32 || a == b {
		t.Fatalf("unexpected generated tokens")
	}
}

func TestOpenCodeEnvironmentDropsCloudCredentials(t *testing.T) {
	got := openCodeEnvironment([]string{
		"Path=C:\\Windows",
		"APPDATA=C:\\Users\\test\\AppData\\Roaming",
		"OPENCODE_CONFIG=C:\\safe.json",
		"OPENAI_API_KEY=cloud",
		"AWS_SECRET_ACCESS_KEY=cloud",
		"CIA_LOCAL_API_KEY=stale",
	})
	want := []string{
		"Path=C:\\Windows",
		"APPDATA=C:\\Users\\test\\AppData\\Roaming",
		"OPENCODE_CONFIG=C:\\safe.json",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("environment = %v, want %v", got, want)
	}
}
