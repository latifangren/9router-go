package semanticcache

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"9router/proxy/internal/fastjson"
	"9router/proxy/internal/translator"
)

// BuildCacheKey generates a deterministic, collision-resistant cache key
// incorporating tenant/session isolation, model, prompt text, tools, and temperature.
func BuildCacheKey(sessionID string, req *translator.OpenAIRequest) string {
	if req == nil {
		return ""
	}

	h := sha256.New()

	// Tenant / session isolation
	if sessionID != "" {
		h.Write([]byte("session:"))
		h.Write([]byte(sessionID))
		h.Write([]byte("\n"))
	}

	// Model
	h.Write([]byte("model:"))
	h.Write([]byte(req.Model))
	h.Write([]byte("\n"))

	// Messages
	for _, m := range req.Messages {
		h.Write([]byte(m.Role))
		h.Write([]byte(":"))
		switch v := m.Content.(type) {
		case string:
			h.Write([]byte(v))
		case []translator.OpenAIContentBlock:
			for _, block := range v {
				if block.Type == "text" {
					h.Write([]byte(block.Text))
				}
			}
		case []any:
			for _, el := range v {
				if bm, ok := el.(map[string]any); ok {
					if bm["type"] == "text" {
						if t, ok := bm["text"].(string); ok {
							h.Write([]byte(t))
						}
					}
				}
			}
		}
		h.Write([]byte("\n"))
	}

	// Tools
	if len(req.Tools) > 0 {
		if tb, err := fastjson.Marshal(req.Tools); err == nil {
			h.Write([]byte("tools:"))
			h.Write(tb)
			h.Write([]byte("\n"))
		}
	}
	if req.ToolChoice != nil {
		if tcb, err := fastjson.Marshal(req.ToolChoice); err == nil {
			h.Write([]byte("tool_choice:"))
			h.Write(tcb)
			h.Write([]byte("\n"))
		}
	}

	// Temperature
	if req.Temperature != nil {
		h.Write([]byte(fmt.Sprintf("temp:%.4f\n", *req.Temperature)))
	}

	return hex.EncodeToString(h.Sum(nil))
}

// ExtractPromptText extracts a normalized text representation of the prompt.
func ExtractPromptText(req *translator.OpenAIRequest) string {
	if req == nil {
		return ""
	}
	var b strings.Builder
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
			for _, el := range v {
				if bm, ok := el.(map[string]any); ok {
					if bm["type"] == "text" {
						if t, ok := bm["text"].(string); ok {
							b.WriteString(t)
						}
					}
				}
			}
		}
		b.WriteByte('\n')
	}
	return b.String()
}
