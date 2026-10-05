package semanticcache

import (
	"time"
)

// Entry holds a cached LLM response alongside its prompt embedding.
type Entry struct {
	Vector       []float32
	Model        string
	PromptText   string
	ResponseBody []byte
	ContentType  string
	StoredAt     time.Time
}
