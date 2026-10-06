package db

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"time"

	"9router/proxy/internal/pricing"
)

// PromptCacheProviderStats holds per-provider cache metrics.
type PromptCacheProviderStats struct {
	Requests            int64 `json:"requests"`
	TotalRequests       int64 `json:"totalRequests"`
	CachedRequests      int64 `json:"cachedRequests"`
	InputTokens         int64 `json:"inputTokens"`
	CachedTokens        int64 `json:"cachedTokens"`
	CacheCreationTokens int64 `json:"cacheCreationTokens"`
}

// PromptCacheModelStats holds per-model cache metrics.
type PromptCacheModelStats struct {
	Requests            int64 `json:"requests"`
	TotalRequests       int64 `json:"totalRequests"`
	CachedRequests      int64 `json:"cachedRequests"`
	InputTokens         int64 `json:"inputTokens"`
	CachedTokens        int64 `json:"cachedTokens"`
	CacheCreationTokens int64 `json:"cacheCreationTokens"`
}

// PromptCacheMetrics holds overall prompt cache statistics.
type PromptCacheMetrics struct {
	TotalRequests            int64                               `json:"totalRequests"`
	RequestsWithCacheControl int64                               `json:"requestsWithCacheControl"`
	TotalInputTokens         int64                               `json:"totalInputTokens"`
	TotalCachedTokens        int64                               `json:"totalCachedTokens"`
	TotalCacheCreationTokens int64                               `json:"totalCacheCreationTokens"`
	TokensSaved              int64                               `json:"tokensSaved"`
	EstimatedCostSaved       float64                             `json:"estimatedCostSaved"`
	ByProvider               map[string]PromptCacheProviderStats `json:"byProvider"`
	ByModel                  map[string]PromptCacheModelStats    `json:"byModel"`
	LastUpdated              string                              `json:"lastUpdated"`
}

// CacheTrendPoint holds a bucketed hourly cache trend point.
type CacheTrendPoint struct {
	Timestamp           string `json:"timestamp"`
	Requests            int64  `json:"requests"`
	CachedRequests      int64  `json:"cachedRequests"`
	InputTokens         int64  `json:"inputTokens"`
	CachedTokens        int64  `json:"cachedTokens"`
	CacheCreationTokens int64  `json:"cacheCreationTokens"`
}

const (
	defaultAvgInputPricePerMillion = 3.0
	defaultCacheSavingsDiscount    = 0.9
)

// GetPromptCacheMetrics aggregates prompt cache statistics from usageHistory.
func (r *Repo) GetPromptCacheMetrics(ctx context.Context) (*PromptCacheMetrics, error) {
	metrics := &PromptCacheMetrics{
		ByProvider:  make(map[string]PromptCacheProviderStats),
		ByModel:     make(map[string]PromptCacheModelStats),
		LastUpdated: time.Now().UTC().Format(time.RFC3339),
	}

	totalsQuery := `
SELECT
	COUNT(*) as totalRequests,
	SUM(CASE WHEN tokens IS NOT NULL AND json_valid(tokens) AND (
		CAST(COALESCE(json_extract(tokens, '$.cached_tokens'), json_extract(tokens, '$.cache_read_input_tokens'), 0) AS INTEGER) > 0
		OR CAST(COALESCE(json_extract(tokens, '$.cache_creation_input_tokens'), json_extract(tokens, '$.cache_creation_tokens'), 0) AS INTEGER) > 0
	) THEN 1 ELSE 0 END) as requestsWithCacheControl,
	COALESCE(SUM(promptTokens), 0) as totalInputTokens,
	COALESCE(SUM(CASE WHEN tokens IS NOT NULL AND json_valid(tokens)
		THEN CAST(COALESCE(json_extract(tokens, '$.cached_tokens'), json_extract(tokens, '$.cache_read_input_tokens'), 0) AS INTEGER)
		ELSE 0 END), 0) as totalCachedTokens,
	COALESCE(SUM(CASE WHEN tokens IS NOT NULL AND json_valid(tokens)
		THEN CAST(COALESCE(json_extract(tokens, '$.cache_creation_input_tokens'), json_extract(tokens, '$.cache_creation_tokens'), 0) AS INTEGER)
		ELSE 0 END), 0) as totalCacheCreationTokens
FROM usageHistory`

	var (
		totalRequests            sql.NullInt64
		requestsWithCacheControl sql.NullInt64
		totalInputTokens         sql.NullInt64
		totalCachedTokens        sql.NullInt64
		totalCacheCreationTokens sql.NullInt64
	)

	row := r.db.QueryRowContext(ctx, totalsQuery)
	if err := row.Scan(
		&totalRequests,
		&requestsWithCacheControl,
		&totalInputTokens,
		&totalCachedTokens,
		&totalCacheCreationTokens,
	); err != nil && err != sql.ErrNoRows {
		return nil, fmt.Errorf("Repo.GetPromptCacheMetrics totals: %w", err)
	}

	metrics.TotalRequests = totalRequests.Int64
	metrics.RequestsWithCacheControl = requestsWithCacheControl.Int64
	metrics.TotalInputTokens = totalInputTokens.Int64
	metrics.TotalCachedTokens = totalCachedTokens.Int64
	metrics.TotalCacheCreationTokens = totalCacheCreationTokens.Int64
	metrics.TokensSaved = metrics.TotalCachedTokens

	pricingQuery := `
SELECT
	COALESCE(provider, 'unknown') as provider,
	COALESCE(model, 'unknown') as model,
	COALESCE(SUM(CASE WHEN tokens IS NOT NULL AND json_valid(tokens)
		THEN CAST(COALESCE(json_extract(tokens, '$.cached_tokens'), json_extract(tokens, '$.cache_read_input_tokens'), 0) AS INTEGER)
		ELSE 0 END), 0) as cachedTokens
FROM usageHistory
WHERE tokens IS NOT NULL AND json_valid(tokens)
GROUP BY provider, model
HAVING cachedTokens > 0`

	var dynamicSavings float64
	pRows, pErr := r.db.QueryContext(ctx, pricingQuery)
	if pErr == nil {
		defer pRows.Close()
		for pRows.Next() {
			var p, m string
			var ct int64
			if err := pRows.Scan(&p, &m, &ct); err == nil && ct > 0 {
				mp, _ := pricing.GetPricingForModel(p, m)
				diff := mp.InputPer1M - mp.CachedPer1M
				if diff <= 0 {
					diff = mp.InputPer1M * defaultCacheSavingsDiscount
				}
				if diff <= 0 {
					diff = defaultAvgInputPricePerMillion * defaultCacheSavingsDiscount
				}
				dynamicSavings += (float64(ct) / 1_000_000.0) * diff
			}
		}
	}
	if dynamicSavings > 0 {
		metrics.EstimatedCostSaved = math.Round(dynamicSavings*100) / 100
	} else {
		savedDollars := (float64(metrics.TokensSaved) / 1_000_000.0) * defaultAvgInputPricePerMillion * defaultCacheSavingsDiscount
		metrics.EstimatedCostSaved = math.Round(savedDollars*100) / 100
	}

	providerQuery := `
SELECT
	COALESCE(provider, 'unknown') as provider,
	COUNT(*) as totalRequests,
	SUM(CASE WHEN tokens IS NOT NULL AND json_valid(tokens) AND (
		CAST(COALESCE(json_extract(tokens, '$.cached_tokens'), json_extract(tokens, '$.cache_read_input_tokens'), 0) AS INTEGER) > 0
		OR CAST(COALESCE(json_extract(tokens, '$.cache_creation_input_tokens'), json_extract(tokens, '$.cache_creation_tokens'), 0) AS INTEGER) > 0
	) THEN 1 ELSE 0 END) as cachedRequests,
	COALESCE(SUM(promptTokens), 0) as inputTokens,
	COALESCE(SUM(CASE WHEN tokens IS NOT NULL AND json_valid(tokens)
		THEN CAST(COALESCE(json_extract(tokens, '$.cached_tokens'), json_extract(tokens, '$.cache_read_input_tokens'), 0) AS INTEGER)
		ELSE 0 END), 0) as cachedTokens,
	COALESCE(SUM(CASE WHEN tokens IS NOT NULL AND json_valid(tokens)
		THEN CAST(COALESCE(json_extract(tokens, '$.cache_creation_input_tokens'), json_extract(tokens, '$.cache_creation_tokens'), 0) AS INTEGER)
		ELSE 0 END), 0) as cacheCreationTokens
FROM usageHistory
WHERE provider IS NOT NULL AND provider != ''
GROUP BY provider`

	rows, err := r.db.QueryContext(ctx, providerQuery)
	if err != nil {
		return nil, fmt.Errorf("Repo.GetPromptCacheMetrics providers: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var (
			p                   string
			pTotalRequests      sql.NullInt64
			pCachedRequests     sql.NullInt64
			pInputTokens        sql.NullInt64
			pCachedTokens       sql.NullInt64
			pCacheCreationTokens sql.NullInt64
		)
		if err := rows.Scan(
			&p,
			&pTotalRequests,
			&pCachedRequests,
			&pInputTokens,
			&pCachedTokens,
			&pCacheCreationTokens,
		); err != nil {
			return nil, fmt.Errorf("Repo.GetPromptCacheMetrics scan provider: %w", err)
		}

		metrics.ByProvider[p] = PromptCacheProviderStats{
			Requests:            pTotalRequests.Int64,
			TotalRequests:       pTotalRequests.Int64,
			CachedRequests:      pCachedRequests.Int64,
			InputTokens:         pInputTokens.Int64,
			CachedTokens:        pCachedTokens.Int64,
			CacheCreationTokens: pCacheCreationTokens.Int64,
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("Repo.GetPromptCacheMetrics iterate providers: %w", err)
	}

	modelQuery := `
SELECT
	COALESCE(model, 'unknown') as model,
	COUNT(*) as totalRequests,
	SUM(CASE WHEN tokens IS NOT NULL AND json_valid(tokens) AND (
		CAST(COALESCE(json_extract(tokens, '$.cached_tokens'), json_extract(tokens, '$.cache_read_input_tokens'), 0) AS INTEGER) > 0
		OR CAST(COALESCE(json_extract(tokens, '$.cache_creation_input_tokens'), json_extract(tokens, '$.cache_creation_tokens'), 0) AS INTEGER) > 0
	) THEN 1 ELSE 0 END) as cachedRequests,
	COALESCE(SUM(promptTokens), 0) as inputTokens,
	COALESCE(SUM(CASE WHEN tokens IS NOT NULL AND json_valid(tokens)
		THEN CAST(COALESCE(json_extract(tokens, '$.cached_tokens'), json_extract(tokens, '$.cache_read_input_tokens'), 0) AS INTEGER)
		ELSE 0 END), 0) as cachedTokens,
	COALESCE(SUM(CASE WHEN tokens IS NOT NULL AND json_valid(tokens)
		THEN CAST(COALESCE(json_extract(tokens, '$.cache_creation_input_tokens'), json_extract(tokens, '$.cache_creation_tokens'), 0) AS INTEGER)
		ELSE 0 END), 0) as cacheCreationTokens
FROM usageHistory
WHERE model IS NOT NULL AND model != ''
GROUP BY model`

	mRows, mErr := r.db.QueryContext(ctx, modelQuery)
	if mErr == nil {
		defer mRows.Close()
		for mRows.Next() {
			var (
				m                    string
				mTotalRequests       sql.NullInt64
				mCachedRequests      sql.NullInt64
				mInputTokens         sql.NullInt64
				mCachedTokens        sql.NullInt64
				mCacheCreationTokens sql.NullInt64
			)
			if err := mRows.Scan(
				&m,
				&mTotalRequests,
				&mCachedRequests,
				&mInputTokens,
				&mCachedTokens,
				&mCacheCreationTokens,
			); err == nil {
				metrics.ByModel[m] = PromptCacheModelStats{
					Requests:            mTotalRequests.Int64,
					TotalRequests:       mTotalRequests.Int64,
					CachedRequests:      mCachedRequests.Int64,
					InputTokens:         mInputTokens.Int64,
					CachedTokens:        mCachedTokens.Int64,
					CacheCreationTokens: mCacheCreationTokens.Int64,
				}
			}
		}
	}

	return metrics, nil
}

// GetPromptCacheTrend returns hourly cache metrics bucketed over the requested hours (1 to 720, default 24).
func (r *Repo) GetPromptCacheTrend(ctx context.Context, hours int) ([]CacheTrendPoint, error) {
	if hours < 1 {
		hours = 24
	}
	if hours > 720 {
		hours = 720
	}

	cutoff := time.Now().UTC().Add(-time.Duration(hours) * time.Hour).Format(time.RFC3339)

	trendQuery := `
SELECT
	strftime('%Y-%m-%dT%H:00:00Z', timestamp) as bucket,
	COUNT(*) as requests,
	SUM(CASE WHEN tokens IS NOT NULL AND json_valid(tokens) AND (
		CAST(COALESCE(json_extract(tokens, '$.cached_tokens'), json_extract(tokens, '$.cache_read_input_tokens'), 0) AS INTEGER) > 0
		OR CAST(COALESCE(json_extract(tokens, '$.cache_creation_input_tokens'), json_extract(tokens, '$.cache_creation_tokens'), 0) AS INTEGER) > 0
	) THEN 1 ELSE 0 END) as cachedRequests,
	COALESCE(SUM(promptTokens), 0) as inputTokens,
	COALESCE(SUM(CASE WHEN tokens IS NOT NULL AND json_valid(tokens)
		THEN CAST(COALESCE(json_extract(tokens, '$.cached_tokens'), json_extract(tokens, '$.cache_read_input_tokens'), 0) AS INTEGER)
		ELSE 0 END), 0) as cachedTokens,
	COALESCE(SUM(CASE WHEN tokens IS NOT NULL AND json_valid(tokens)
		THEN CAST(COALESCE(json_extract(tokens, '$.cache_creation_input_tokens'), json_extract(tokens, '$.cache_creation_tokens'), 0) AS INTEGER)
		ELSE 0 END), 0) as cacheCreationTokens
FROM usageHistory
WHERE timestamp >= ?
GROUP BY bucket
ORDER BY bucket ASC`

	rows, err := r.db.QueryContext(ctx, trendQuery, cutoff)
	if err != nil {
		return nil, fmt.Errorf("Repo.GetPromptCacheTrend: %w", err)
	}
	defer rows.Close()

	points := make([]CacheTrendPoint, 0)
	for rows.Next() {
		var pt CacheTrendPoint
		var (
			bucket              sql.NullString
			requests            sql.NullInt64
			cachedRequests      sql.NullInt64
			inputTokens         sql.NullInt64
			cachedTokens        sql.NullInt64
			cacheCreationTokens sql.NullInt64
		)
		if err := rows.Scan(
			&bucket,
			&requests,
			&cachedRequests,
			&inputTokens,
			&cachedTokens,
			&cacheCreationTokens,
		); err != nil {
			return nil, fmt.Errorf("Repo.GetPromptCacheTrend scan: %w", err)
		}
		pt.Timestamp = bucket.String
		pt.Requests = requests.Int64
		pt.CachedRequests = cachedRequests.Int64
		pt.InputTokens = inputTokens.Int64
		pt.CachedTokens = cachedTokens.Int64
		pt.CacheCreationTokens = cacheCreationTokens.Int64
		points = append(points, pt)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("Repo.GetPromptCacheTrend iterate: %w", err)
	}

	return points, nil
}
