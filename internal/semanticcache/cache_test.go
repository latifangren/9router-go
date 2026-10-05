package semanticcache

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"9router/proxy/internal/translator"
)

func TestHashEmbedder_Deterministic(t *testing.T) {
	emb := NewHashEmbedder(32)
	ctx := context.Background()

	v1, err := emb.Embed(ctx, "hello world")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	v2, err := emb.Embed(ctx, "hello world")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	sim := CosineSimilarity(v1, v2)
	if sim < 0.9999 {
		t.Fatalf("expected identical embeddings to have ~1.0 cosine similarity, got %f", sim)
	}

	v3, err := emb.Embed(ctx, "completely different query")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	diffSim := CosineSimilarity(v1, v3)
	if diffSim > 0.9 {
		t.Fatalf("expected different text to have low similarity, got %f", diffSim)
	}
}

func TestSemanticCache_ExactMatch(t *testing.T) {
	ctx := context.Background()
	cache := New(Config{
		Enabled:             true,
		SimilarityThreshold: 0.95,
		TTL:                 time.Hour,
		MaxEntries:          100,
	}, nil, nil)

	req := &translator.OpenAIRequest{
		Model: "gpt-4o",
		Messages: []translator.OpenAIMessage{
			{Role: "user", Content: "What is 2 + 2?"},
		},
	}

	// 1. Lookup miss initially
	entry, score, ok := cache.Lookup(ctx, req)
	if ok || entry != nil {
		t.Fatalf("expected cache miss, got hit")
	}

	// 2. Store response
	respBody := []byte(`{"choices":[{"message":{"role":"assistant","content":"4"}}]}`)
	if err := cache.Store(ctx, req, respBody, "application/json"); err != nil {
		t.Fatalf("failed to store in cache: %v", err)
	}

	// 3. Lookup hit
	entry, score, ok = cache.Lookup(ctx, req)
	if !ok || entry == nil {
		t.Fatalf("expected cache hit, got miss")
	}
	if score < 0.99 {
		t.Fatalf("expected score close to 1.0, got %f", score)
	}
	if string(entry.ResponseBody) != string(respBody) {
		t.Fatalf("expected response body %s, got %s", respBody, entry.ResponseBody)
	}

	// 4. Model mismatch should miss
	reqOtherModel := &translator.OpenAIRequest{
		Model: "claude-3-5-sonnet",
		Messages: []translator.OpenAIMessage{
			{Role: "user", Content: "What is 2 + 2?"},
		},
	}
	_, _, ok = cache.Lookup(ctx, reqOtherModel)
	if ok {
		t.Fatalf("expected miss when querying with different model")
	}
}

func TestSemanticCache_StreamBypass(t *testing.T) {
	ctx := context.Background()
	cache := New(Config{Enabled: true}, nil, nil)

	streamReq := &translator.OpenAIRequest{
		Model:  "gpt-4o",
		Stream: true,
		Messages: []translator.OpenAIMessage{
			{Role: "user", Content: "hello"},
		},
	}

	if _, _, ok := cache.Lookup(ctx, streamReq); ok {
		t.Fatalf("expected stream request to bypass cache lookup")
	}
	if err := cache.Store(ctx, streamReq, []byte("ok"), "application/json"); err != nil {
		t.Fatalf("unexpected error on stream store: %v", err)
	}
	if cache.Len() != 0 {
		t.Fatalf("expected store to ignore stream request")
	}
}

func TestSemanticCache_TTL(t *testing.T) {
	ctx := context.Background()
	cache := New(Config{
		Enabled:             true,
		SimilarityThreshold: 0.95,
		TTL:                 10 * time.Millisecond,
		MaxEntries:          10,
	}, nil, nil)

	req := &translator.OpenAIRequest{
		Model: "gpt-4o",
		Messages: []translator.OpenAIMessage{
			{Role: "user", Content: "quick expiring test"},
		},
	}

	_ = cache.Store(ctx, req, []byte(`{"res":"ok"}`), "application/json")

	_, _, ok := cache.Lookup(ctx, req)
	if !ok {
		t.Fatalf("expected immediate lookup hit")
	}

	time.Sleep(20 * time.Millisecond)

	_, _, ok = cache.Lookup(ctx, req)
	if ok {
		t.Fatalf("expected expired entry to result in cache miss")
	}
}

func TestSemanticCache_ConcurrentAccess(t *testing.T) {
	ctx := context.Background()
	cache := New(Config{
		Enabled:             true,
		SimilarityThreshold: 0.90,
		TTL:                 time.Minute,
		MaxEntries:          200,
	}, nil, nil)

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(2)
		workerID := i
		go func() {
			defer wg.Done()
			req := &translator.OpenAIRequest{
				Model: "gpt-4o",
				Messages: []translator.OpenAIMessage{
					{Role: "user", Content: fmt.Sprintf("Query prompt number %d", workerID%5)},
				},
			}
			cache.Lookup(ctx, req)
		}()
		go func() {
			defer wg.Done()
			req := &translator.OpenAIRequest{
				Model: "gpt-4o",
				Messages: []translator.OpenAIMessage{
					{Role: "user", Content: fmt.Sprintf("Query prompt number %d", workerID%5)},
				},
			}
			_ = cache.Store(ctx, req, []byte(fmt.Sprintf(`{"worker":%d}`, workerID)), "application/json")
		}()
	}
	wg.Wait()
}
