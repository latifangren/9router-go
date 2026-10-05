package semanticcache

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/translator"
)

func TestSemanticCache_ExactMatch(t *testing.T) {
	cache := New(Config{
		Enabled:    true,
		TTL:        time.Hour,
		MaxEntries: 10,
	}, nil)

	ctx := context.Background()
	req := &translator.OpenAIRequest{
		Model: "gpt-4o",
		Messages: []translator.OpenAIMessage{
			{Role: "user", Content: "Hello world"},
		},
	}

	// 1. Initial lookup should miss
	if _, _, hit := cache.Lookup(ctx, req); hit {
		t.Fatal("expected cache miss on empty cache")
	}

	// 2. Store response
	respBody := []byte(`{"id":"chatcmpl-1","choices":[{"message":{"role":"assistant","content":"Hi there"}}]}`)
	if err := cache.Store(ctx, req, respBody, "application/json"); err != nil {
		t.Fatalf("Store failed: %v", err)
	}

	// 3. Lookup should hit
	entry, score, hit := cache.Lookup(ctx, req)
	if !hit {
		t.Fatal("expected cache hit after Store")
	}
	if score != 1.0 {
		t.Errorf("expected score 1.0, got %f", score)
	}
	if string(entry.ResponseBody) != string(respBody) {
		t.Errorf("expected %s, got %s", respBody, entry.ResponseBody)
	}

	// 4. Modifying returned ResponseBody should not mutate stored cache (bytes.Clone check)
	entry.ResponseBody[0] = 'X'
	entry2, _, _ := cache.Lookup(ctx, req)
	if entry2.ResponseBody[0] == 'X' {
		t.Fatal("cache returned mutable reference instead of cloned bytes")
	}
}

func TestSemanticCache_TenantIsolation(t *testing.T) {
	cache := New(Config{
		Enabled:    true,
		TTL:        time.Hour,
		MaxEntries: 10,
	}, nil)

	req := &translator.OpenAIRequest{
		Model: "gpt-4o",
		Messages: []translator.OpenAIMessage{
			{Role: "user", Content: "Private data query"},
		},
	}

	ctxUserA := handlerutil.WithSessionID(context.Background(), "user-a")
	ctxUserB := handlerutil.WithSessionID(context.Background(), "user-b")

	respA := []byte(`{"id":"chatcmpl-a","content":"User A secret"}`)
	_ = cache.Store(ctxUserA, req, respA, "application/json")

	// User A should hit
	if _, _, hit := cache.Lookup(ctxUserA, req); !hit {
		t.Fatal("User A should hit own cache")
	}

	// User B should miss (strict tenant isolation)
	if _, _, hit := cache.Lookup(ctxUserB, req); hit {
		t.Fatal("User B must NOT see User A's cached response")
	}
}

func TestSemanticCache_StreamBypass(t *testing.T) {
	cache := New(Config{
		Enabled:    true,
		TTL:        time.Hour,
		MaxEntries: 10,
	}, nil)

	ctx := context.Background()
	req := &translator.OpenAIRequest{
		Model:  "gpt-4o",
		Stream: true,
		Messages: []translator.OpenAIMessage{
			{Role: "user", Content: "Stream query"},
		},
	}

	// Store should be ignored
	_ = cache.Store(ctx, req, []byte("stream response"), "application/json")
	if cache.Len() != 0 {
		t.Fatalf("expected stream request not to be stored, len=%d", cache.Len())
	}

	// Lookup should be ignored
	if _, _, hit := cache.Lookup(ctx, req); hit {
		t.Fatal("expected stream lookup to be bypassed")
	}
}

func TestSemanticCache_TTLExpiration(t *testing.T) {
	cache := New(Config{
		Enabled:    true,
		TTL:        30 * time.Millisecond,
		MaxEntries: 10,
	}, nil)

	ctx := context.Background()
	req := &translator.OpenAIRequest{
		Model: "gpt-4o",
		Messages: []translator.OpenAIMessage{
			{Role: "user", Content: "Expiring query"},
		},
	}

	_ = cache.Store(ctx, req, []byte("data"), "application/json")
	if _, _, hit := cache.Lookup(ctx, req); !hit {
		t.Fatal("expected immediate hit")
	}

	time.Sleep(50 * time.Millisecond)
	if _, _, hit := cache.Lookup(ctx, req); hit {
		t.Fatal("expected cache miss after TTL expiry")
	}
}

func TestSemanticCache_LRUEviction(t *testing.T) {
	cache := New(Config{
		Enabled:    true,
		TTL:        time.Hour,
		MaxEntries: 2,
	}, nil)

	ctx := context.Background()
	req1 := &translator.OpenAIRequest{Model: "gpt-4o", Messages: []translator.OpenAIMessage{{Role: "user", Content: "q1"}}}
	req2 := &translator.OpenAIRequest{Model: "gpt-4o", Messages: []translator.OpenAIMessage{{Role: "user", Content: "q2"}}}
	req3 := &translator.OpenAIRequest{Model: "gpt-4o", Messages: []translator.OpenAIMessage{{Role: "user", Content: "q3"}}}

	_ = cache.Store(ctx, req1, []byte("r1"), "application/json")
	_ = cache.Store(ctx, req2, []byte("r2"), "application/json")

	// Access req1 to make req2 the oldest
	_, _, _ = cache.Lookup(ctx, req1)

	// Add req3, should evict req2
	_ = cache.Store(ctx, req3, []byte("r3"), "application/json")

	if _, _, hit := cache.Lookup(ctx, req1); !hit {
		t.Fatal("req1 should still be present")
	}
	if _, _, hit := cache.Lookup(ctx, req2); hit {
		t.Fatal("req2 should have been evicted by LRU")
	}
	if _, _, hit := cache.Lookup(ctx, req3); !hit {
		t.Fatal("req3 should be present")
	}
}

func TestSemanticCache_ConcurrentParallelAccess(t *testing.T) {
	cache := New(Config{
		Enabled:    true,
		TTL:        time.Hour,
		MaxEntries: 100,
	}, nil)

	const goroutines = 50
	const iterations = 50
	var wg sync.WaitGroup
	wg.Add(goroutines * 2)

	// Concurrent writers
	for i := range goroutines {
		go func(id int) {
			defer wg.Done()
			for j := range iterations {
				req := &translator.OpenAIRequest{
					Model: "gpt-4o",
					Messages: []translator.OpenAIMessage{
						{Role: "user", Content: fmt.Sprintf("concurrent query %d", j%10)},
					},
				}
				_ = cache.Store(context.Background(), req, []byte(fmt.Sprintf("resp %d-%d", id, j)), "application/json")
			}
		}(i)
	}

	// Concurrent readers
	for range goroutines {
		go func() {
			defer wg.Done()
			for j := range iterations {
				req := &translator.OpenAIRequest{
					Model: "gpt-4o",
					Messages: []translator.OpenAIMessage{
						{Role: "user", Content: fmt.Sprintf("concurrent query %d", j%10)},
					},
				}
				_, _, _ = cache.Lookup(context.Background(), req)
			}
		}()
	}

	wg.Wait()
	if cache.Len() > 100 {
		t.Fatalf("cache size %d exceeded maxEntries 100", cache.Len())
	}
}
