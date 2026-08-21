package pipeline

import "testing"

func TestFanInOrder(t *testing.T) {
	in := make([]int, 500)
	for i := range in {
		in[i] = i
	}
	got := FanIn(in, 8, func(v int) int { return v * 3 })
	for i, v := range got {
		if v != i*3 {
			t.Fatalf("index %d: got %d want %d", i, v, i*3)
		}
	}
}

func TestFanInEmpty(t *testing.T) {
	if got := FanIn(nil, 4, func(v int) int { return v }); len(got) != 0 {
		t.Fatalf("expected empty, got %v", got)
	}
}

func TestFanInSingleWorker(t *testing.T) {
	got := FanIn([]int{1, 2, 3}, 1, func(v int) int { return v + 1 })
	want := []int{2, 3, 4}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("index %d: got %d want %d", i, got[i], want[i])
		}
	}
}
