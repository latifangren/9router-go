package semanticcache

import (
	"context"

	"9router/proxy/internal/translator"
)

type cacheReqKey struct{}

// WithCachedRequest attaches an OpenAIRequest to the context for downstream caching.
func WithCachedRequest(ctx context.Context, req *translator.OpenAIRequest) context.Context {
	if req == nil {
		return ctx
	}
	return context.WithValue(ctx, cacheReqKey{}, req)
}

// FromContext extracts an OpenAIRequest attached for downstream caching.
func FromContext(ctx context.Context) *translator.OpenAIRequest {
	if ctx == nil {
		return nil
	}
	if v, ok := ctx.Value(cacheReqKey{}).(*translator.OpenAIRequest); ok {
		return v
	}
	return nil
}
