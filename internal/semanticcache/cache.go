package semanticcache

import (
	"context"
	"time"

	"9router/proxy/internal/translator"
)

// Config configures semantic cache behavior.
type Config struct {
	Enabled             bool
	SimilarityThreshold float64       // default 0.95
	TTL                 time.Duration // default 24h
	MaxEntries          int           // default 1000
}

// Cache coordinates embedder, store, and caching policy.
type Cache struct {
	cfg      Config
	store    Store
	embedder Embedder
}

// New creates a semantic cache with the provided store and embedder.
func New(cfg Config, store Store, embedder Embedder) *Cache {
	if cfg.SimilarityThreshold <= 0 {
		cfg.SimilarityThreshold = 0.95
	}
	if cfg.TTL <= 0 {
		cfg.TTL = 24 * time.Hour
	}
	if cfg.MaxEntries <= 0 {
		cfg.MaxEntries = 1000
	}
	if store == nil {
		store = NewMemoryStore(cfg.MaxEntries, cfg.TTL)
	}
	if embedder == nil {
		embedder = NewHashEmbedder(32)
	}
	return &Cache{
		cfg:      cfg,
		store:    store,
		embedder: embedder,
	}
}

// Enabled returns true if the cache is active.
func (c *Cache) Enabled() bool {
	return c != nil && c.cfg.Enabled
}

// Lookup queries the cache for a similar cached response.
func (c *Cache) Lookup(ctx context.Context, req *translator.OpenAIRequest) (*Entry, float64, bool) {
	if !c.Enabled() || req == nil || req.Stream {
		return nil, 0, false
	}

	text := ExtractPromptText(req)
	if text == "" {
		return nil, 0, false
	}

	vec, err := c.embedder.Embed(ctx, text)
	if err != nil || len(vec) == 0 {
		return nil, 0, false
	}

	entry, score, ok, err := c.store.Nearest(ctx, vec)
	if err != nil || !ok {
		return nil, 0, false
	}

	// Verify model matches (different models should not share output)
	if entry.Model != "" && req.Model != "" && entry.Model != req.Model {
		return nil, 0, false
	}

	if score < c.cfg.SimilarityThreshold {
		return nil, 0, false
	}

	if c.cfg.TTL > 0 && time.Since(entry.StoredAt) > c.cfg.TTL {
		return nil, 0, false
	}

	return &entry, score, true
}

// Store saves a response body and content type into the cache.
func (c *Cache) Store(ctx context.Context, req *translator.OpenAIRequest, responseBody []byte, contentType string) error {
	if !c.Enabled() || req == nil || req.Stream || len(responseBody) == 0 {
		return nil
	}

	text := ExtractPromptText(req)
	if text == "" {
		return nil
	}

	vec, err := c.embedder.Embed(ctx, text)
	if err != nil || len(vec) == 0 {
		return err
	}

	if contentType == "" {
		contentType = "application/json"
	}

	return c.store.Put(ctx, Entry{
		Vector:       vec,
		Model:        req.Model,
		PromptText:   text,
		ResponseBody: responseBody,
		ContentType:  contentType,
		StoredAt:     time.Now(),
	})
}

// Len returns the number of cached entries.
func (c *Cache) Len() int {
	if c == nil || c.store == nil {
		return 0
	}
	return c.store.Len()
}
