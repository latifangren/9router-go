package db

import (
	"context"
	"testing"
	"time"
)

func TestCompressionAnalytics_InsertAndSummary(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()

	if err := EnsureCoreSchema(database); err != nil {
		t.Fatalf("ensure schema: %v", err)
	}
	repo := NewRepo(database)
	ctx := context.Background()

	// Seed 2 records
	rec1 := CompressionAnalyticsRecord{
		Timestamp:              time.Now().UTC().Format(time.RFC3339),
		Provider:               "anthropic",
		Mode:                   "rtk",
		OriginalTokens:         1000,
		CompressedTokens:       600,
		TokensSaved:            400,
		DurationMs:             30,
		RequestID:              "req-1",
		ActualPromptTokens:     600,
		ActualCompletionTokens: 200,
		ActualTotalTokens:      800,
	}
	if err := repo.InsertCompressionAnalytics(ctx, rec1); err != nil {
		t.Fatalf("insert rec1: %v", err)
	}

	rec2 := CompressionAnalyticsRecord{
		Timestamp:              time.Now().UTC().Format(time.RFC3339),
		Provider:               "openai",
		Mode:                   "caveman",
		OriginalTokens:         500,
		CompressedTokens:       350,
		TokensSaved:            150,
		DurationMs:             20,
		RequestID:              "req-2",
		ActualPromptTokens:     350,
		ActualCompletionTokens: 100,
		ActualTotalTokens:      450,
	}
	if err := repo.InsertCompressionAnalytics(ctx, rec2); err != nil {
		t.Fatalf("insert rec2: %v", err)
	}

	summary, err := repo.GetCompressionAnalyticsSummary(ctx, "24h")
	if err != nil {
		t.Fatalf("get summary: %v", err)
	}

	if summary.TotalRequests != 2 {
		t.Errorf("totalRequests = %d, want 2", summary.TotalRequests)
	}
	if summary.TotalTokensSaved != 550 {
		t.Errorf("totalTokensSaved = %d, want 550", summary.TotalTokensSaved)
	}
	if summary.AvgSavingsPct < 30 || summary.AvgSavingsPct > 40 {
		t.Errorf("avgSavingsPct = %f, expected between 30 and 40", summary.AvgSavingsPct)
	}

	// Verify Mode breakdown
	rtkStats, ok := summary.ByMode["rtk"]
	if !ok || rtkStats.TokensSaved != 400 {
		t.Errorf("byMode[rtk] = %+v, want 400 saved", rtkStats)
	}

	cavemanStats, ok := summary.ByMode["caveman"]
	if !ok || cavemanStats.TokensSaved != 150 {
		t.Errorf("byMode[caveman] = %+v, want 150 saved", cavemanStats)
	}

	// Verify Provider breakdown
	if summary.ByProvider["anthropic"].TokensSaved != 400 {
		t.Errorf("byProvider[anthropic] = %d, want 400", summary.ByProvider["anthropic"].TokensSaved)
	}
	if summary.ByProvider["openai"].TokensSaved != 150 {
		t.Errorf("byProvider[openai] = %d, want 150", summary.ByProvider["openai"].TokensSaved)
	}

	// Verify Real Usage
	if summary.RealUsage.RequestsWithReceipts != 2 {
		t.Errorf("requestsWithReceipts = %d, want 2", summary.RealUsage.RequestsWithReceipts)
	}
	if summary.RealUsage.PromptTokens != 950 {
		t.Errorf("promptTokens = %d, want 950", summary.RealUsage.PromptTokens)
	}
}

func TestCompressionAnalytics_UsageHistoryBackfill(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()

	if err := EnsureCoreSchema(database); err != nil {
		t.Fatalf("ensure schema: %v", err)
	}
	repo := NewRepo(database)
	ctx := context.Background()

	// Seed usageHistory with saved_tokens using Go 1.22 range loop
	for i := range 3 {
		err := repo.InsertUsageHistory(
			"anthropic", "claude-3-5", "conn-1", "key-1", "/v1/messages",
			600, 200, 0.05, "success", 800, "{}",
			`{"original_input_tokens":1000,"compressed_input_tokens":600,"saved_tokens":400,"saved_percent":40}`,
		)
		if err != nil {
			t.Fatalf("seed usageHistory %d: %v", i, err)
		}
	}

	summary, err := repo.GetCompressionAnalyticsSummary(ctx, "24h")
	if err != nil {
		t.Fatalf("get summary backfill: %v", err)
	}

	if summary.TotalRequests != 3 {
		t.Errorf("totalRequests = %d, want 3", summary.TotalRequests)
	}
	if summary.TotalTokensSaved != 1200 {
		t.Errorf("totalTokensSaved = %d, want 1200", summary.TotalTokensSaved)
	}
	if summary.AvgSavingsPct != 40 {
		t.Errorf("avgSavingsPct = %f, want 40", summary.AvgSavingsPct)
	}
}
