package db

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"time"
)

// CompressionModeStats holds per-mode compression statistics.
type CompressionModeStats struct {
	Count         int64   `json:"count"`
	TokensSaved   int64   `json:"tokensSaved"`
	AvgSavingsPct float64 `json:"avgSavingsPct"`
	Skipped       int64   `json:"skipped,omitempty"`
}

// CompressionProviderStats holds per-provider compression statistics.
type CompressionProviderStats struct {
	Count       int64 `json:"count"`
	TokensSaved int64 `json:"tokensSaved"`
}

// CompressionHourBucket represents an hourly time bucket for trend charts.
type CompressionHourBucket struct {
	Hour        string `json:"hour"`
	Count       int64  `json:"count"`
	TokensSaved int64  `json:"tokensSaved"`
}

// CompressionRealUsage aggregates upstream LLM token counts and dollar savings.
type CompressionRealUsage struct {
	RequestsWithReceipts int64            `json:"requestsWithReceipts"`
	PromptTokens         int64            `json:"promptTokens"`
	CompletionTokens     int64            `json:"completionTokens"`
	TotalTokens          int64            `json:"totalTokens"`
	CacheReadTokens      int64            `json:"cacheReadTokens"`
	CacheWriteTokens     int64            `json:"cacheWriteTokens"`
	EstimatedUsdSaved    float64          `json:"estimatedUsdSaved"`
	BySource             map[string]int64 `json:"bySource"`
}

// CompressionAnalyticsSummary matches the OmniRoute summary payload.
type CompressionAnalyticsSummary struct {
	TotalRequests       int64                               `json:"totalRequests"`
	TotalTokensSaved    int64                               `json:"totalTokensSaved"`
	AvgSavingsPct       float64                             `json:"avgSavingsPct"`
	AvgDurationMs       int64                               `json:"avgDurationMs"`
	ByMode              map[string]CompressionModeStats     `json:"byMode"`
	ByProvider          map[string]CompressionProviderStats `json:"byProvider"`
	Last24h             []CompressionHourBucket             `json:"last24h"`
	TotalSkipped        int64                               `json:"totalSkipped"`
	BySkipReason        map[string]int64                    `json:"bySkipReason,omitempty"`
	ValidationFallbacks int64                               `json:"validationFallbacks"`
	RealUsage           CompressionRealUsage                `json:"realUsage"`
}

// CompressionAnalyticsRecord represents a single run to record in compressionAnalytics table.
type CompressionAnalyticsRecord struct {
	Timestamp              string
	Provider               string
	Mode                   string
	OriginalTokens         int
	CompressedTokens       int
	TokensSaved            int
	DurationMs             int
	RequestID              string
	ActualPromptTokens     int
	ActualCompletionTokens int
	ActualTotalTokens      int
	ActualCacheReadTokens  int
	ActualCacheWriteTokens int
	SkipReason             string
}

// InsertCompressionAnalytics inserts a telemetry record of a compression run.
func (r *Repo) InsertCompressionAnalytics(ctx context.Context, rec CompressionAnalyticsRecord) error {
	if rec.Timestamp == "" {
		rec.Timestamp = time.Now().UTC().Format(time.RFC3339)
	}
	if rec.Mode == "" {
		rec.Mode = "rtk"
	}

	query := `
INSERT INTO compressionAnalytics (
	timestamp, provider, mode, originalTokens, compressedTokens, tokensSaved,
	durationMs, requestId, actualPromptTokens, actualCompletionTokens,
	actualTotalTokens, actualCacheReadTokens, actualCacheWriteTokens, skipReason
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`

	_, err := r.db.ExecContext(ctx, query,
		rec.Timestamp,
		rec.Provider,
		rec.Mode,
		rec.OriginalTokens,
		rec.CompressedTokens,
		rec.TokensSaved,
		rec.DurationMs,
		rec.RequestID,
		rec.ActualPromptTokens,
		rec.ActualCompletionTokens,
		rec.ActualTotalTokens,
		rec.ActualCacheReadTokens,
		rec.ActualCacheWriteTokens,
		rec.SkipReason,
	)
	if err != nil {
		return fmt.Errorf("Repo.InsertCompressionAnalytics: %w", err)
	}
	return nil
}

// GetCompressionAnalyticsSummary returns aggregated metrics from compressionAnalytics (or usageHistory backfill).
func (r *Repo) GetCompressionAnalyticsSummary(ctx context.Context, since string) (*CompressionAnalyticsSummary, error) {
	summary := &CompressionAnalyticsSummary{
		ByMode:       make(map[string]CompressionModeStats),
		ByProvider:   make(map[string]CompressionProviderStats),
		Last24h:      make([]CompressionHourBucket, 0),
		BySkipReason: make(map[string]int64),
		RealUsage: CompressionRealUsage{
			BySource: make(map[string]int64),
		},
	}

	cutoff := ""
	now := time.Now().UTC()
	switch since {
	case "7d":
		cutoff = now.Add(-7 * 24 * time.Hour).Format(time.RFC3339)
	case "30d":
		cutoff = now.Add(-30 * 24 * time.Hour).Format(time.RFC3339)
	case "all":
		cutoff = ""
	default: // "24h"
		cutoff = now.Add(-24 * time.Hour).Format(time.RFC3339)
	}

	// 1. Check if compressionAnalytics has records
	var caCount int64
	if cutoff != "" {
		_ = r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM compressionAnalytics WHERE timestamp >= ?`, cutoff).Scan(&caCount)
	} else {
		_ = r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM compressionAnalytics`).Scan(&caCount)
	}

	if caCount > 0 {
		return r.queryCompressionAnalyticsTable(ctx, summary, cutoff)
	}

	// Fallback/backfill: aggregate from usageHistory where saved_tokens > 0
	return r.queryUsageHistoryBackfill(ctx, summary, cutoff)
}

func (r *Repo) queryCompressionAnalyticsTable(ctx context.Context, summary *CompressionAnalyticsSummary, cutoff string) (*CompressionAnalyticsSummary, error) {
	whereClause := ""
	args := []any{}
	if cutoff != "" {
		whereClause = "WHERE timestamp >= ?"
		args = append(args, cutoff)
	}

	// Scalars
	scalarQuery := fmt.Sprintf(`
SELECT
	COUNT(*) as total,
	COALESCE(SUM(tokensSaved), 0) as totalSaved,
	COALESCE(AVG(CASE WHEN originalTokens > 0 THEN (tokensSaved * 100.0) / originalTokens ELSE 0 END), 0) as avgPct,
	COALESCE(AVG(durationMs), 0) as avgDur,
	COALESCE(SUM(CASE WHEN skipReason IS NOT NULL AND skipReason != '' THEN 1 ELSE 0 END), 0) as skipped
FROM compressionAnalytics %s`, whereClause)

	var (
		total      sql.NullInt64
		totalSaved sql.NullInt64
		avgPct     sql.NullFloat64
		avgDur     sql.NullFloat64
		skipped    sql.NullInt64
	)
	if err := r.db.QueryRowContext(ctx, scalarQuery, args...).Scan(&total, &totalSaved, &avgPct, &avgDur, &skipped); err != nil && err != sql.ErrNoRows {
		return nil, fmt.Errorf("queryCompressionAnalyticsTable scalars: %w", err)
	}

	summary.TotalRequests = total.Int64
	summary.TotalTokensSaved = totalSaved.Int64
	summary.AvgSavingsPct = math.Round(avgPct.Float64)
	summary.AvgDurationMs = int64(math.Round(avgDur.Float64))
	summary.TotalSkipped = skipped.Int64

	// By Mode
	modeQuery := fmt.Sprintf(`
SELECT
	mode,
	COUNT(*) as cnt,
	COALESCE(SUM(tokensSaved), 0) as saved,
	COALESCE(AVG(CASE WHEN originalTokens > 0 THEN (tokensSaved * 100.0) / originalTokens ELSE 0 END), 0) as avgPct,
	COALESCE(SUM(CASE WHEN skipReason IS NOT NULL AND skipReason != '' THEN 1 ELSE 0 END), 0) as skp
FROM compressionAnalytics %s
GROUP BY mode`, whereClause)

	modeRows, err := r.db.QueryContext(ctx, modeQuery, args...)
	if err == nil {
		defer modeRows.Close()
		for modeRows.Next() {
			var m string
			var cnt, saved, skp sql.NullInt64
			var pct sql.NullFloat64
			if err := modeRows.Scan(&m, &cnt, &saved, &pct, &skp); err == nil {
				summary.ByMode[m] = CompressionModeStats{
					Count:         cnt.Int64,
					TokensSaved:   saved.Int64,
					AvgSavingsPct: math.Round(pct.Float64),
					Skipped:       skp.Int64,
				}
			}
		}
	}

	// By Provider
	provWhere := "WHERE provider IS NOT NULL AND provider != ''"
	provArgs := []any{}
	if cutoff != "" {
		provWhere += " AND timestamp >= ?"
		provArgs = append(provArgs, cutoff)
	}
	provQuery := fmt.Sprintf(`
SELECT
	COALESCE(provider, 'unknown') as prov,
	COUNT(*) as cnt,
	COALESCE(SUM(tokensSaved), 0) as saved
FROM compressionAnalytics
%s
GROUP BY prov`, provWhere)

	provRows, err := r.db.QueryContext(ctx, provQuery, provArgs...)
	if err == nil {
		defer provRows.Close()
		for provRows.Next() {
			var p string
			var cnt, saved sql.NullInt64
			if err := provRows.Scan(&p, &cnt, &saved); err == nil {
				summary.ByProvider[p] = CompressionProviderStats{
					Count:       cnt.Int64,
					TokensSaved: saved.Int64,
				}
			}
		}
	}

	// Hourly trend
	trendCutoff := cutoff
	if trendCutoff == "" {
		trendCutoff = time.Now().UTC().Add(-24 * time.Hour).Format(time.RFC3339)
	}
	trendQuery := `
SELECT
	strftime('%Y-%m-%dT%H:00:00Z', timestamp) as hr,
	COUNT(*) as cnt,
	COALESCE(SUM(tokensSaved), 0) as saved
FROM compressionAnalytics
WHERE timestamp >= ?
GROUP BY hr
ORDER BY hr ASC`

	trendRows, err := r.db.QueryContext(ctx, trendQuery, trendCutoff)
	if err == nil {
		defer trendRows.Close()
		for trendRows.Next() {
			var hr string
			var cnt, saved sql.NullInt64
			if err := trendRows.Scan(&hr, &cnt, &saved); err == nil {
				summary.Last24h = append(summary.Last24h, CompressionHourBucket{
					Hour:        hr,
					Count:       cnt.Int64,
					TokensSaved: saved.Int64,
				})
			}
		}
	}

	// Real Usage Receipts
	receiptWhere := "WHERE actualPromptTokens > 0 OR actualTotalTokens > 0"
	receiptArgs := []any{}
	if cutoff != "" {
		receiptWhere += " AND timestamp >= ?"
		receiptArgs = append(receiptArgs, cutoff)
	}
	receiptQuery := fmt.Sprintf(`
SELECT
	COUNT(*) as receipts,
	COALESCE(SUM(actualPromptTokens), 0) as prompt,
	COALESCE(SUM(actualCompletionTokens), 0) as completion,
	COALESCE(SUM(actualTotalTokens), 0) as total,
	COALESCE(SUM(actualCacheReadTokens), 0) as cacheRead,
	COALESCE(SUM(actualCacheWriteTokens), 0) as cacheWrite
FROM compressionAnalytics
%s`, receiptWhere)

	var (
		rReceipts, rPrompt, rComp, rTotal, rRead, rWrite sql.NullInt64
	)
	if err := r.db.QueryRowContext(ctx, receiptQuery, receiptArgs...).Scan(&rReceipts, &rPrompt, &rComp, &rTotal, &rRead, &rWrite); err == nil {
		summary.RealUsage.RequestsWithReceipts = rReceipts.Int64
		summary.RealUsage.PromptTokens = rPrompt.Int64
		summary.RealUsage.CompletionTokens = rComp.Int64
		summary.RealUsage.TotalTokens = rTotal.Int64
		summary.RealUsage.CacheReadTokens = rRead.Int64
		summary.RealUsage.CacheWriteTokens = rWrite.Int64
		summary.RealUsage.EstimatedUsdSaved = math.Round((float64(summary.TotalTokensSaved)/1_000_000.0)*3.0*100) / 100
	}

	return summary, nil
}

func (r *Repo) queryUsageHistoryBackfill(ctx context.Context, summary *CompressionAnalyticsSummary, cutoff string) (*CompressionAnalyticsSummary, error) {
	whereClause := `WHERE tokens IS NOT NULL AND json_valid(tokens) AND CAST(COALESCE(json_extract(tokens, '$.saved_tokens'), 0) AS INTEGER) > 0`
	args := []any{}
	if cutoff != "" {
		whereClause += " AND timestamp >= ?"
		args = append(args, cutoff)
	}

	scalarQuery := fmt.Sprintf(`
SELECT
	COUNT(*) as total,
	COALESCE(SUM(CAST(json_extract(tokens, '$.saved_tokens') AS INTEGER)), 0) as totalSaved,
	COALESCE(AVG(CAST(COALESCE(json_extract(tokens, '$.saved_percent'), 0) AS REAL)), 0) as avgPct
FROM usageHistory %s`, whereClause)

	var (
		total      sql.NullInt64
		totalSaved sql.NullInt64
		avgPct     sql.NullFloat64
	)
	if err := r.db.QueryRowContext(ctx, scalarQuery, args...).Scan(&total, &totalSaved, &avgPct); err != nil && err != sql.ErrNoRows {
		return nil, fmt.Errorf("queryUsageHistoryBackfill scalars: %w", err)
	}

	summary.TotalRequests = total.Int64
	summary.TotalTokensSaved = totalSaved.Int64
	summary.AvgSavingsPct = math.Round(avgPct.Float64)
	summary.AvgDurationMs = 25 // Default heuristic for local token compression

	if summary.TotalRequests > 0 {
		summary.ByMode["rtk"] = CompressionModeStats{
			Count:         summary.TotalRequests,
			TokensSaved:   summary.TotalTokensSaved,
			AvgSavingsPct: summary.AvgSavingsPct,
		}
	}

	// Providers
	provQuery := fmt.Sprintf(`
SELECT
	COALESCE(provider, 'unknown') as prov,
	COUNT(*) as cnt,
	COALESCE(SUM(CAST(json_extract(tokens, '$.saved_tokens') AS INTEGER)), 0) as saved
FROM usageHistory %s
GROUP BY prov`, whereClause)

	provRows, err := r.db.QueryContext(ctx, provQuery, args...)
	if err == nil {
		defer provRows.Close()
		for provRows.Next() {
			var p string
			var cnt, saved sql.NullInt64
			if err := provRows.Scan(&p, &cnt, &saved); err == nil {
				summary.ByProvider[p] = CompressionProviderStats{
					Count:       cnt.Int64,
					TokensSaved: saved.Int64,
				}
			}
		}
	}

	// Hourly trend
	trendCutoff := cutoff
	if trendCutoff == "" {
		trendCutoff = time.Now().UTC().Add(-24 * time.Hour).Format(time.RFC3339)
	}
	trendQuery := `
SELECT
	strftime('%Y-%m-%dT%H:00:00Z', timestamp) as hr,
	COUNT(*) as cnt,
	COALESCE(SUM(CAST(json_extract(tokens, '$.saved_tokens') AS INTEGER)), 0) as saved
FROM usageHistory
WHERE tokens IS NOT NULL AND json_valid(tokens)
  AND CAST(COALESCE(json_extract(tokens, '$.saved_tokens'), 0) AS INTEGER) > 0
  AND timestamp >= ?
GROUP BY hr
ORDER BY hr ASC`

	trendRows, err := r.db.QueryContext(ctx, trendQuery, trendCutoff)
	if err == nil {
		defer trendRows.Close()
		for trendRows.Next() {
			var hr string
			var cnt, saved sql.NullInt64
			if err := trendRows.Scan(&hr, &cnt, &saved); err == nil {
				summary.Last24h = append(summary.Last24h, CompressionHourBucket{
					Hour:        hr,
					Count:       cnt.Int64,
					TokensSaved: saved.Int64,
				})
			}
		}
	}

	// Real Usage
	summary.RealUsage.RequestsWithReceipts = summary.TotalRequests
	summary.RealUsage.EstimatedUsdSaved = math.Round((float64(summary.TotalTokensSaved)/1_000_000.0)*3.0*100) / 100

	return summary, nil
}
