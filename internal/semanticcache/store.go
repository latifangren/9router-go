package semanticcache

import (
	"bytes"
	"container/list"
	"context"
	"sync"
	"time"
)

// Store is the storage interface for cache entries.
type Store interface {
	Get(ctx context.Context, key string) (Entry, bool)
	Put(ctx context.Context, key string, e Entry) error
	Len() int
	Clear()
}

type lruItem struct {
	key   string
	entry Entry
}

// LRUStore is a thread-safe, bounded in-memory LRU cache store.
type LRUStore struct {
	mu         sync.RWMutex
	items      map[string]*list.Element
	evictList  *list.List
	maxEntries int
	ttl        time.Duration
}

// NewLRUStore creates a thread-safe LRUStore bounded by maxEntries and ttl.
func NewLRUStore(maxEntries int, ttl time.Duration) *LRUStore {
	if maxEntries <= 0 {
		maxEntries = 1000
	}
	return &LRUStore{
		items:      make(map[string]*list.Element, min(maxEntries, 128)),
		evictList:  list.New(),
		maxEntries: maxEntries,
		ttl:        ttl,
	}
}

// Get returns the cached entry for key, or false if missing or expired.
// Clones ResponseBody to prevent data races across goroutines.
func (s *LRUStore) Get(_ context.Context, key string) (Entry, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	elem, ok := s.items[key]
	if !ok {
		return Entry{}, false
	}

	item := elem.Value.(*lruItem)
	if s.ttl > 0 && time.Since(item.entry.StoredAt) > s.ttl {
		s.removeElement(elem)
		return Entry{}, false
	}

	s.evictList.MoveToFront(elem)
	cloned := item.entry
	cloned.ResponseBody = bytes.Clone(item.entry.ResponseBody)
	return cloned, true
}

// Put inserts or updates a cache entry.
// Clones ResponseBody before storing.
func (s *LRUStore) Put(_ context.Context, key string, e Entry) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	e.ResponseBody = bytes.Clone(e.ResponseBody)
	if e.StoredAt.IsZero() {
		e.StoredAt = time.Now()
	}

	// Update existing entry
	if elem, ok := s.items[key]; ok {
		s.evictList.MoveToFront(elem)
		elem.Value.(*lruItem).entry = e
		return nil
	}

	// Evict oldest if capacity reached
	for s.maxEntries > 0 && s.evictList.Len() >= s.maxEntries {
		s.removeOldest()
	}

	item := &lruItem{key: key, entry: e}
	elem := s.evictList.PushFront(item)
	s.items[key] = elem
	return nil
}

func (s *LRUStore) removeElement(elem *list.Element) {
	s.evictList.Remove(elem)
	item := elem.Value.(*lruItem)
	delete(s.items, item.key)
}

func (s *LRUStore) removeOldest() {
	elem := s.evictList.Back()
	if elem != nil {
		s.removeElement(elem)
	}
}

// Len returns the current number of cached entries.
func (s *LRUStore) Len() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.items)
}

// Clear flushes all cached entries.
func (s *LRUStore) Clear() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.items = make(map[string]*list.Element)
	s.evictList.Init()
}
