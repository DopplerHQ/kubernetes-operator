package cache

import (
	"container/list"
	"sync"
)

type Key struct {
	Identity string
	// Audiences is part of the cache key because it includes the token secret's
	// namespace and name (e.g., "dopplerTokenSecret:namespace:secret-name").
	// This allows the same identity to be used in different token secrets
	// (different names or namespaces) while maintaining separate cached providers
	// for each, since each will have different audience claims in their JWTs.
	Audiences string
}

type Cache[T any] struct {
	mu      sync.RWMutex
	items   map[Key]*list.Element
	lruList *list.List
	maxSize int
	onEvict func(T)
}

type cacheEntry[T any] struct {
	key   Key
	value T
}

func New[T any](maxSize int, onEvict func(T)) *Cache[T] {
	return &Cache[T]{
		items:   make(map[Key]*list.Element),
		lruList: list.New(),
		maxSize: maxSize,
		onEvict: onEvict,
	}
}

func (c *Cache[T]) Get(key Key) (T, bool) {
	if c.maxSize == 0 {
		var zero T
		return zero, false
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if elem, exists := c.items[key]; exists {
		entry := elem.Value.(*cacheEntry[T])
		c.lruList.MoveToFront(elem)
		return entry.value, true
	}

	var zero T
	return zero, false
}

func (c *Cache[T]) Add(key Key, value T) {
	if c.maxSize == 0 {
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if elem, exists := c.items[key]; exists {
		entry := elem.Value.(*cacheEntry[T])
		entry.value = value
		c.lruList.MoveToFront(elem)
		return
	}

	// Add new entry
	entry := &cacheEntry[T]{
		key:   key,
		value: value,
	}
	elem := c.lruList.PushFront(entry)
	c.items[key] = elem

	// Check if we need to evict
	if c.lruList.Len() > c.maxSize {
		c.evictOldest()
	}
}

func (c *Cache[T]) Remove(key Key) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if elem, exists := c.items[key]; exists {
		c.removeElement(elem)
	}
}

// Remove element from cache
func (c *Cache[T]) removeElement(elem *list.Element) {
	entry := elem.Value.(*cacheEntry[T])
	delete(c.items, entry.key)
	c.lruList.Remove(elem)
	if c.onEvict != nil {
		c.onEvict(entry.value)
	}
}

// Remove the least recently used item
func (c *Cache[T]) evictOldest() {
	elem := c.lruList.Back()
	if elem != nil {
		c.removeElement(elem)
	}
}
