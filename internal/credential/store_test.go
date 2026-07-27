package credential

import "testing"

func TestTarget(t *testing.T) {
	t.Parallel()
	got, err := Target(" Inference ")
	if err != nil {
		t.Fatal(err)
	}
	if got != "CIA Local AI/inference" {
		t.Fatalf("Target() = %q", got)
	}
	if _, err := Target("cloud"); err == nil {
		t.Fatal("Target(cloud) succeeded")
	}
}
