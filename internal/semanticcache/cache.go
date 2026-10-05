package semanticcache

import (
	"context"
	"time"

	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/translator"
)

// Cache coordinates prompt/response caching with thread-safety and TTL support.
type Cache struct {
	cfg       Config
	store     Store
	enabledFn func() bool
}

// New creates a Cache instance with default config and optional enabled predicate.
func New(cfg Config, store Store, enabledFn ...func() bool) *Cache {
	if cfg.MaxEntries <= 0 {
		cfg.MaxEntries = 1000
	}
	if cfg.TTL <= 0 {
		cfg.TTL = 24 * time.Hour
	}
	if cfg.SimilarityThreshold <= 0 {
		cfg.SimilarityThreshold = 0.95
	}
	if store == nil {
		store = NewLRUStore(cfg.MaxEntries, cfg.TTL)
	}
	var fn func() bool
	if len(enabledFn) > 0 {
		fn = enabledFn[0]
	}
	return &Cache{
		cfg:       cfg,
		store:     store,
		enabledFn: fn,
	}
}

// Enabled reports whether the cache is active.
func (c *Cache) Enabled() bool {
	if c == nil {
		return false
	}
	if c.enabledFn != nil {
		return c.enabledFn()
	}
	return c.cfg.Enabled
}

// Lookup queries the cache for a stored response.
func (c *Cache) Lookup(ctx context.Context, req *translator.OpenAIRequest) (*Entry, float64, bool) {
	if !c.Enabled() || req == nil || req.Stream {
		return nil, 0, false
	}

	sessionID := handlerutil.GetSessionID(ctx)
	key := BuildCacheKey(sessionID, req)
	if key == "" {
		return nil, 0, false
	}

	entry, ok := c.store.Get(ctx, key)
	if !ok {
		return nil, 0, false
	}

	// Verify model matches
	if entry.Model != "" && req.Model != "" && entry.Model != req.Model {
		return nil, 0, false
	}

	return &entry, 1.0, true
}

// Store saves a response body into the cache.
func (c *Cache) Store(ctx context.Context, req *translator.OpenAIRequest, responseBody []byte, contentType string) error {
	if !c.Enabled() || req == nil || req.Stream || len(responseBody) == 0 {
		return nil
	}

	sessionID := handlerutil.GetSessionID(ctx)
	key := BuildCacheKey(sessionID, req)
	if key == "" {
		return nil
	}

	if contentType == "" {
		contentType = "application/json"
	}

	return c.store.Put(ctx, key, Entry{
		Key:          key,
		Model:        req.Model,
		ResponseBody: responseBody,
		ContentType:  contentType,
		StoredAt:     time.Now(),
	})
}

// Clear flushes all entries from the store.
func (c *Cache) Clear() {
	if c != nil && c.store != nil {
		c.store.Clear()
	}
}

// Len returns the number of cached entries.
func (c *Cache) Len() int {
	if c == nil || c.store == nil {
		return 0
	}
	return c.store.Len()
}
