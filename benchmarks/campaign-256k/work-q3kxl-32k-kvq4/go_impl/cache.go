package cache

import "container/list"

// LRU is a fixed-capacity least-recently-used cache.
type LRU struct {
	capacity int
	items    map[string]*list.Element
	order    *list.List
}

type entry struct {
	key   string
	value int
}

// NewLRU creates a new LRU cache with the given capacity.
// If capacity is negative, it is treated as zero.
func NewLRU(capacity int) *LRU {
	if capacity < 0 {
		capacity = 0
	}
	return &LRU{
		capacity: capacity,
		items:    make(map[string]*list.Element),
		order:    list.New(),
	}
}

// Get returns the value for key and marks it as most recently used.
func (c *LRU) Get(key string) (int, bool) {
	if c == nil || c.capacity <= 0 {
		return 0, false
	}

	elem, ok := c.items[key]
	if !ok {
		return 0, false
	}

	c.order.MoveToFront(elem)
	return elem.Value.(entry).value, true
}

// Put inserts or updates a key/value pair.
// If the cache is full, the least recently used entry is evicted.
func (c *LRU) Put(key string, value int) {
	if c == nil || c.capacity <= 0 {
		return
	}

	if elem, ok := c.items[key]; ok {
		c.order.MoveToFront(elem)
		elem.Value = entry{key: key, value: value}
		return
	}

	if c.order.Len() == c.capacity {
		oldest := c.order.Back()
		if oldest != nil {
			c.order.Remove(oldest)
			delete(c.items, oldest.Value.(entry).key)
		}
	}

	c.items[key] = c.order.PushFront(entry{key: key, value: value})
}

// Len returns the number of entries currently in the cache.
func (c *LRU) Len() int {
	if c == nil {
		return 0
	}
	return len(c.items)
}
