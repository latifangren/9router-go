package semanticcache

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"strings"

	"9router/proxy/internal/translator"
)

// Embedder maps prompt text to a vector for cache lookup.
type Embedder interface {
	Embed(ctx context.Context, text string) ([]float32, error)
}

// ExtractPromptText extracts a normalized, deterministic prompt string from an OpenAIRequest.
func ExtractPromptText(req *translator.OpenAIRequest) string {
	if req == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString("model:")
	b.WriteString(req.Model)
	b.WriteByte('\n')

	for _, m := range req.Messages {
		b.WriteString(m.Role)
		b.WriteByte(':')
		switch v := m.Content.(type) {
		case string:
			b.WriteString(v)
		case []translator.OpenAIContentBlock:
			for _, block := range v {
				if block.Type == "text" {
					b.WriteString(block.Text)
				}
			}
		case []any:
			for _, item := range v {
				if blockMap, ok := item.(map[string]any); ok {
					if text, ok := blockMap["text"].(string); ok {
						b.WriteString(text)
					}
				}
			}
		}
		b.WriteByte('\n')
	}
	return b.String()
}

// HashEmbedder is a dependency-free deterministic embedder based on SHA-256.
type HashEmbedder struct {
	dims int
}

// NewHashEmbedder creates a new HashEmbedder with the given dimension count.
func NewHashEmbedder(dims int) *HashEmbedder {
	if dims <= 0 {
		dims = 32
	}
	return &HashEmbedder{dims: dims}
}

// Embed maps text to a deterministic float32 vector in [-1, 1].
func (h *HashEmbedder) Embed(_ context.Context, text string) ([]float32, error) {
	vec := make([]float32, h.dims)
	for i := 0; i < h.dims; i++ {
		var seed [8]byte
		binary.LittleEndian.PutUint64(seed[:], uint64(i))
		sum := sha256.Sum256(append([]byte(text), seed[:]...))
		u := binary.LittleEndian.Uint32(sum[:4])
		vec[i] = float32(int32(u)) / float32(1<<31)
	}
	return vec, nil
}
