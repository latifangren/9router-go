package semanticcache

import (
	"context"
	"math"
	"sync"
	"time"
)

// Store is the storage backend for semantic cache entries.
type Store interface {
	Nearest(ctx context.Context, vec []float32) (Entry, float64, bool, error)
	Put(ctx context.Context, e Entry) error
	Len() int
}

// CosineSimilarity computes the cosine similarity between two float vectors.
func CosineSimilarity(a, b []float32) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		va, vb := float64(a[i]), float64(b[i])
		dot += va * vb
		na += va * va
		nb += vb * vb
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}

// MemoryStore is an in-memory vector store with ring buffer and lazy TTL eviction.
type MemoryStore struct {
	mu      sync.RWMutex
	entries []Entry
	max     int
	cursor  int
	count   int
	ttl     time.Duration
}

// NewMemoryStore creates a thread-safe bounded MemoryStore.
func NewMemoryStore(max int, ttl time.Duration) *MemoryStore {
	if max <= 0 {
		max = 1000
	}
	return &MemoryStore{
		max:     max,
		ttl:     ttl,
		entries: make([]Entry, 0, min(max, 128)),
	}
}

func (m *MemoryStore) evict(now time.Time) {
	if m.ttl <= 0 || len(m.entries) == 0 {
		return
	}
	alive := m.entries[:0]
	for _, e := range m.entries {
		if now.Sub(e.StoredAt) <= m.ttl {
			alive = append(alive, e)
		}
	}
	removed := len(m.entries) - len(alive)
	if removed > 0 {
		m.entries = alive
		m.count -= removed
		if m.cursor >= len(m.entries) {
			m.cursor = 0
		}
	}
}

const evictEvery = 50

// Put inserts an entry into the store.
func (m *MemoryStore) Put(_ context.Context, e Entry) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.count > 0 && m.count%evictEvery == 0 {
		m.evict(time.Now())
	}

	if m.max > 0 && len(m.entries) >= m.max {
		m.entries[m.cursor] = e
		m.cursor = (m.cursor + 1) % m.max
		return nil
	}

	m.entries = append(m.entries, e)
	m.count++
	return nil
}

// Nearest returns the entry with the highest cosine similarity to vec.
func (m *MemoryStore) Nearest(_ context.Context, vec []float32) (Entry, float64, bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if len(m.entries) == 0 {
		return Entry{}, 0, false, nil
	}

	now := time.Now()
	var best Entry
	bestScore := -2.0

	for _, e := range m.entries {
		if m.ttl > 0 && now.Sub(e.StoredAt) > m.ttl {
			continue
		}
		score := CosineSimilarity(vec, e.Vector)
		if score > bestScore {
			bestScore = score
			best = e
		}
	}

	if bestScore < -1.0 {
		return Entry{}, 0, false, nil
	}
	return best, bestScore, true, nil
}

// Len returns the current number of entries stored.
func (m *MemoryStore) Len() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.entries)
}
