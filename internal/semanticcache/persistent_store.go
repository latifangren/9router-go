package semanticcache

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"time"
)

// PersistentLRUStore wraps an in-memory LRUStore with SQLite disk persistence.
type PersistentLRUStore struct {
	lru         *LRUStore
	db          *sql.DB
	ttl         time.Duration
	stopJanitor chan struct{}
	closeOnce   sync.Once
}

// NewPersistentStore creates an LRUStore backed by SQLite disk persistence.
func NewPersistentStore(db *sql.DB, maxEntries int, ttl time.Duration) *PersistentLRUStore {
	store := &PersistentLRUStore{
		lru:         NewLRUStore(maxEntries, ttl),
		db:          db,
		ttl:         ttl,
		stopJanitor: make(chan struct{}),
	}
	_ = store.Hydrate(context.Background())
	if ttl > 0 {
		store.startJanitor(1 * time.Hour)
	}
	return store
}

func (p *PersistentLRUStore) startJanitor(interval time.Duration) {
	if interval <= 0 {
		interval = 1 * time.Hour
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-p.stopJanitor:
				return
			case <-ticker.C:
				if p.ttl > 0 {
					_ = p.InvalidateOlderThan(context.Background(), p.ttl)
				}
			}
		}
	}()
}

// Close stops the background janitor cleanly.
func (p *PersistentLRUStore) Close() {
	p.closeOnce.Do(func() {
		if p.stopJanitor != nil {
			close(p.stopJanitor)
		}
	})
}

// Hydrate preloads unexpired entries from SQLite into the in-memory LRU cache.
func (p *PersistentLRUStore) Hydrate(ctx context.Context) error {
	if p.db == nil {
		return nil
	}

	cutoff := ""
	if p.ttl > 0 {
		cutoff = time.Now().Add(-p.ttl).UTC().Format(time.RFC3339)
	}

	query := `
SELECT key, model, responseBody, contentType, storedAt, hitCount, tokensSaved
FROM semanticCacheEntries`
	args := []any{}
	if cutoff != "" {
		query += " WHERE storedAt >= ?"
		args = append(args, cutoff)
	}

	rows, err := p.db.QueryContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("PersistentLRUStore.Hydrate: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var (
			key, model, contentType, storedAtStr string
			body                                  []byte
			hitCount, tokensSaved                 int64
		)
		if err := rows.Scan(&key, &model, &body, &contentType, &storedAtStr, &hitCount, &tokensSaved); err == nil {
			storedAt, _ := time.Parse(time.RFC3339, storedAtStr)
			if storedAt.IsZero() {
				storedAt = time.Now()
			}
			_ = p.lru.Put(ctx, key, Entry{
				Key:          key,
				Model:        model,
				ResponseBody: body,
				ContentType:  contentType,
				StoredAt:     storedAt,
				HitCount:     hitCount,
				TokensSaved:  tokensSaved,
			})
		}
	}
	return nil
}

// Get queries the in-memory LRU cache.
func (p *PersistentLRUStore) Get(ctx context.Context, key string) (Entry, bool) {
	return p.lru.Get(ctx, key)
}

// Put updates in-memory LRU and writes through to SQLite.
func (p *PersistentLRUStore) Put(ctx context.Context, key string, e Entry) error {
	if err := p.lru.Put(ctx, key, e); err != nil {
		return err
	}
	if p.db != nil {
		storedAtStr := e.StoredAt.UTC().Format(time.RFC3339)
		_, _ = p.db.ExecContext(ctx, `
INSERT OR REPLACE INTO semanticCacheEntries (
	key, model, responseBody, contentType, storedAt, hitCount, tokensSaved
) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			key, e.Model, e.ResponseBody, e.ContentType, storedAtStr, e.HitCount, e.TokensSaved,
		)
	}
	return nil
}

// Delete removes from in-memory LRU and SQLite.
func (p *PersistentLRUStore) Delete(ctx context.Context, key string) bool {
	ok := p.lru.Delete(ctx, key)
	if p.db != nil {
		_, _ = p.db.ExecContext(ctx, `DELETE FROM semanticCacheEntries WHERE key = ?`, key)
	}
	return ok
}

// Len returns the in-memory entry count.
func (p *PersistentLRUStore) Len() int {
	return p.lru.Len()
}

// DBLen returns the persistent SQLite entry count.
func (p *PersistentLRUStore) DBLen() int {
	if p.db == nil {
		return 0
	}
	var count int
	_ = p.db.QueryRow(`SELECT COUNT(*) FROM semanticCacheEntries`).Scan(&count)
	return count
}

// Clear removes all entries from both memory and SQLite.
func (p *PersistentLRUStore) Clear() {
	p.lru.Clear()
	if p.db != nil {
		_, _ = p.db.Exec(`DELETE FROM semanticCacheEntries`)
	}
}

// Entries returns active entries from the in-memory LRU.
func (p *PersistentLRUStore) Entries() []Entry {
	return p.lru.Entries()
}

// InvalidateByModel deletes matching entries in both memory and SQLite.
func (p *PersistentLRUStore) InvalidateByModel(ctx context.Context, model string) int {
	count := p.lru.InvalidateByModel(ctx, model)
	if p.db != nil {
		_, _ = p.db.ExecContext(ctx, `DELETE FROM semanticCacheEntries WHERE model = ?`, model)
	}
	return count
}

// InvalidateOlderThan deletes expired entries in both memory and SQLite.
func (p *PersistentLRUStore) InvalidateOlderThan(ctx context.Context, d time.Duration) int {
	count := p.lru.InvalidateOlderThan(ctx, d)
	if p.db != nil {
		cutoff := time.Now().Add(-d).UTC().Format(time.RFC3339)
		_, _ = p.db.ExecContext(ctx, `DELETE FROM semanticCacheEntries WHERE storedAt < ?`, cutoff)
	}
	return count
}

// RecordHit increments hit counters in memory and SQLite.
func (p *PersistentLRUStore) RecordHit(ctx context.Context, key string, tokensSaved int64) {
	p.lru.RecordHit(ctx, key, tokensSaved)
	if p.db != nil {
		_, _ = p.db.ExecContext(ctx, `
UPDATE semanticCacheEntries
SET hitCount = hitCount + 1, tokensSaved = tokensSaved + ?
WHERE key = ?`, tokensSaved, key)
	}
}
