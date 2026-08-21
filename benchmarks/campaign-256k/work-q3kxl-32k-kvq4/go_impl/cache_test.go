package cache

import "testing"

func TestLRUBasic(t *testing.T) {
	c := NewLRU(2)
	c.Put("a", 1)
	c.Put("b", 2)
	if v, ok := c.Get("a"); !ok || v != 1 {
		t.Fatalf("a: got %v %v", v, ok)
	}
	c.Put("c", 3) // evicts b, because a was just read
	if _, ok := c.Get("b"); ok {
		t.Fatal("b should have been evicted")
	}
	if v, ok := c.Get("c"); !ok || v != 3 {
		t.Fatalf("c: got %v %v", v, ok)
	}
	if c.Len() != 2 {
		t.Fatalf("len: got %d want 2", c.Len())
	}
}

func TestLRUUpdateDoesNotGrow(t *testing.T) {
	c := NewLRU(2)
	c.Put("a", 1)
	c.Put("a", 9)
	if c.Len() != 1 {
		t.Fatalf("len: got %d want 1", c.Len())
	}
	if v, _ := c.Get("a"); v != 9 {
		t.Fatalf("a: got %v want 9", v)
	}
}

func TestLRUEvictionOrder(t *testing.T) {
	c := NewLRU(3)
	c.Put("x", 1)
	c.Put("y", 2)
	c.Put("z", 3)
	c.Get("x")
	c.Put("w", 4) // y is now the least recently used
	if _, ok := c.Get("y"); ok {
		t.Fatal("y should have been evicted")
	}
	if _, ok := c.Get("x"); !ok {
		t.Fatal("x should have survived")
	}
}
