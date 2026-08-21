package cache

import "container/list"

type entry struct {
	key   string
	value int
}

type LRU struct {
	capacity int
	items    map[string]*list.Element
	list     *list.List
}

func NewLRU(capacity int) *LRU {
	if capacity < 0 {
		capacity = 0
	}

	return &LRU{
		capacity: capacity,
		items:    make(map[string]*list.Element),
		list:     list.New(),
	}
}

func (c *LRU) Get(key string) (int, bool) {
	elem, ok := c.items[key]
	if !ok {
		return 0, false
	}

	c.list.MoveToFront(elem)
	return elem.Value.(entry).value, true
}

func (c *LRU) Put(key string, value int) {
	if c.capacity == 0 {
		return
	}

	if elem, ok := c.items[key]; ok {
		c.list.MoveToFront(elem)
		elem.Value = entry{key: key, value: value}
		return
	}

	if c.list.Len() >= c.capacity {
		if back := c.list.Back(); back != nil {
			c.list.Remove(back)
			delete(c.items, back.Value.(entry).key)
		}
	}

	c.items[key] = c.list.PushFront(entry{key: key, value: value})
}

func (c *LRU) Len() int {
	return len(c.items)
}
