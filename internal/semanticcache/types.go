package semanticcache

import (
	"time"
)

// Entry represents a cached chat completion response.
type Entry struct {
	Key          string
	Model        string
	ResponseBody []byte
	ContentType  string
	StoredAt     time.Time
}

// Config configures semantic/prompt cache behavior.
type Config struct {
	Enabled             bool
	SimilarityThreshold float64
	TTL                 time.Duration
	MaxEntries          int
}
