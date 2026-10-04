package dashboard

import (
	json "encoding/json/v2"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"9router/proxy/internal/db"
	"9router/proxy/internal/handlerutil"
)

// countProxyPoolBindings counts provider connections bound to the given pool,
// checking both the top-level proxyPoolId and providerSpecificData.proxyPoolId.
func (h *DashboardHandler) countProxyPoolBindings() map[string]int {
	boundCounts := make(map[string]int)
	conns, _ := h.Repo.GetProviderConnections("", false)
	for _, c := range conns {
		if c == nil {
			continue
		}
		var dataMap map[string]any
		if c.Data != "" {
			_ = json.Unmarshal([]byte(c.Data), &dataMap)
		}
		poolID := handlerutil.GetString(dataMap, "proxyPoolId")
		if poolID == "" {
			if psd, ok := dataMap["providerSpecificData"].(map[string]any); ok {
				poolID = handlerutil.GetString(psd, "proxyPoolId")
			}
		}
		if poolID != "" {
			boundCounts[poolID]++
		}
	}
	return boundCounts
}

// HandleGetProxyPools handles GET /api/proxy-pools.
func (h *DashboardHandler) HandleGetProxyPools(w http.ResponseWriter, r *http.Request) {
	includeUsage := r.URL.Query().Get("includeUsage") == "true"
	pools, err := h.Repo.ListProxyPools()
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	if includeUsage {
		boundCounts := h.countProxyPoolBindings()

		for _, p := range pools {
			if id, ok := p["id"].(string); ok {
				p["boundConnectionCount"] = boundCounts[id]
			}
		}
	}

	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"proxyPools": pools,
	})
}

// HandleCreateProxyPool handles POST /api/proxy-pools.
func (h *DashboardHandler) HandleCreateProxyPool(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "failed to read body")
		return
	}
	defer r.Body.Close()

	var req struct {
		Name        string   `json:"name"`
		ProxyURL    string   `json:"proxyUrl"`
		URLs        []string `json:"urls"`
		Type        string   `json:"type"`
		NoProxy     string   `json:"noProxy"`
		StrictProxy bool     `json:"strictProxy"`
		IsActive    bool     `json:"isActive"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	if req.Name == "" {
		req.Name = "Proxy Pool"
	}
	if req.Type == "" {
		if strings.HasPrefix(req.ProxyURL, "socks") {
			req.Type = "socks5"
		} else {
			req.Type = "http"
		}
	}

	poolData := db.ProxyPoolData{
		Name:        req.Name,
		ProxyURL:    req.ProxyURL,
		NoProxy:     req.NoProxy,
		Type:        req.Type,
		StrictProxy: req.StrictProxy,
	}

	created, err := h.Repo.InsertProxyPool(poolData)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	handlerutil.WriteJSON(w, http.StatusOK, created)
}

// HandleUpdateProxyPool handles PUT /api/proxy-pools/{id}.
func (h *DashboardHandler) HandleUpdateProxyPool(w http.ResponseWriter, r *http.Request) {
	id := getURLParam(r, "id")
	if id == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "missing proxy pool id")
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "failed to read body")
		return
	}
	defer r.Body.Close()

	var updates map[string]any
	if err := json.Unmarshal(body, &updates); err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	if err := h.Repo.UpdateProxyPool(id, updates); err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{"success": true})
}

// HandleDeleteProxyPool handles DELETE /api/proxy-pools/{id}.
// Upstream parity: refuse with 409 while connections are still bound to the pool.
func (h *DashboardHandler) HandleDeleteProxyPool(w http.ResponseWriter, r *http.Request) {
	id := getURLParam(r, "id")
	if id == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "missing proxy pool id")
		return
	}

	if bound := h.countProxyPoolBindings()[id]; bound > 0 {
		handlerutil.WriteJSON(w, http.StatusConflict, map[string]any{
			"error":                "Proxy pool is currently in use",
			"boundConnectionCount": bound,
		})
		return
	}

	if err := h.Repo.DeleteProxyPool(id); err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{"success": true})
}

// HandleTestProxyPool handles POST /api/proxy-pools/{id}/test.
func (h *DashboardHandler) HandleTestProxyPool(w http.ResponseWriter, r *http.Request) {
	id := getURLParam(r, "id")
	if id == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "missing proxy pool id")
		return
	}

	pool, err := h.Repo.GetProxyPool(id)
	if err != nil || pool == nil {
		handlerutil.WriteJSONError(w, http.StatusNotFound, "proxy pool not found")
		return
	}

	targetURL := pool.NextURL()
	if targetURL == "" {
		_ = h.Repo.SetProxyPoolStatus(id, "failed", 0)
		handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
			"success": false,
			"status":  "failed",
			"latency": int64(0),
			"error":   "no proxy URLs configured",
		})
		return
	}

	// Upstream parity: relay-type pools (vercel/cloudflare/deno) are not HTTP
	// proxies but plain HTTPS endpoints that forward based on x-relay-*
	// headers. Dialing them via http.ProxyURL breaks with
	// "malformed HTTP status code", so they get the upstream relay test.
	if pool.IsEdgeRelay() {
		h.testRelayPool(w, r, id, targetURL)
		return
	}

	primaryProbeURL := h.PrimaryProbeURL
	if primaryProbeURL == "" {
		primaryProbeURL = DefaultPrimaryProbeURL
	}
	secondaryProbeURL := h.SecondaryProbeURL
	if secondaryProbeURL == "" {
		secondaryProbeURL = DefaultSecondaryProbeURL
	}

	start := time.Now()
	proxyParsed, err := url.Parse(targetURL)
	if err != nil {
		_ = h.Repo.SetProxyPoolStatus(id, "failed", 0)
		handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
			"success": false,
			"status":  "failed",
			"latency": int64(0),
			"error":   "invalid proxy URL format",
		})
		return
	}

	tr := &http.Transport{
		Proxy:             http.ProxyURL(proxyParsed),
		DisableKeepAlives: true,
	}
	defer tr.CloseIdleConnections()

	client := &http.Client{
		Transport: tr,
		Timeout:   5 * time.Second,
	}

	reqPrimary, err := http.NewRequestWithContext(r.Context(), http.MethodGet, primaryProbeURL, nil)
	if err != nil {
		_ = h.Repo.SetProxyPoolStatus(id, "failed", 0)
		handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
			"success": false,
			"status":  "failed",
			"latency": int64(0),
			"error":   err.Error(),
		})
		return
	}

	resp, err := client.Do(reqPrimary)
	latencyMs := time.Since(start).Milliseconds()

	// Primary probe succeeded
	if err == nil && resp != nil && resp.StatusCode < 400 {
		_ = resp.Body.Close()
		_ = h.Repo.SetProxyPoolStatus(id, "passed", latencyMs)
		handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
			"success": true,
			"status":  "passed",
			"latency": latencyMs,
		})
		return
	}

	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}

	// Primary probe failed or timed out. Attempt secondary probe to avoid false negatives when Google is blocked.
	secStart := time.Now()
	reqSec, errReqSec := http.NewRequestWithContext(r.Context(), http.MethodGet, secondaryProbeURL, nil)
	if errReqSec != nil {
		_ = h.Repo.SetProxyPoolStatus(id, "failed", 0)
		handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
			"success": false,
			"status":  "failed",
			"latency": int64(0),
			"error":   errReqSec.Error(),
		})
		return
	}

	respSec, errSec := client.Do(reqSec)
	if errSec == nil && respSec != nil && respSec.StatusCode < 400 {
		_ = respSec.Body.Close()
		latencyMs = time.Since(secStart).Milliseconds()
		_ = h.Repo.SetProxyPoolStatus(id, "passed", latencyMs)
		handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
			"success": true,
			"status":  "passed",
			"latency": latencyMs,
		})
		return
	}

	if respSec != nil && respSec.Body != nil {
		_ = respSec.Body.Close()
	}

	// Both probes failed
	status := "failed"
	_ = h.Repo.SetProxyPoolStatus(id, status, 0)
	errStr := "connection timed out or failed"
	if errSec != nil {
		errStr = errSec.Error()
	} else if err != nil {
		errStr = err.Error()
	}
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"success": false,
		"status":  status,
		"latency": int64(0),
		"error":   errStr,
	})
}

// testRelayPool tests a deploy-relay pool (vercel/cloudflare/deno) the way
// upstream does: a plain GET to the relay URL with x-relay-target and
// x-relay-path headers (mirrors testVercelRelay in the Next.js dashboard).
func (h *DashboardHandler) testRelayPool(w http.ResponseWriter, r *http.Request, id, relayURL string) {
	start := time.Now()
	finish := func(ok bool, errStr string) {
		latencyMs := time.Since(start).Milliseconds()
		status := "passed"
		var lastError any
		if !ok {
			status = "failed"
			lastError = errStr
			latencyMs = 0
		}
		_ = h.Repo.UpdateProxyPool(id, map[string]any{
			"testStatus":   status,
			"lastTestedAt": time.Now().UTC().Format(time.RFC3339),
			"lastError":    lastError,
			"latency":      latencyMs,
			"isActive":     ok,
		})
		body := map[string]any{"success": ok, "status": status, "latency": latencyMs}
		if !ok {
			body["error"] = errStr
		}
		handlerutil.WriteJSON(w, http.StatusOK, body)
	}

	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, relayURL, nil)
	if err != nil {
		finish(false, "invalid relay URL format")
		return
	}
	req.Header.Set("x-relay-target", "https://httpbin.org")
	req.Header.Set("x-relay-path", "/get")
	tr := &http.Transport{
		DisableKeepAlives: true,
	}
	defer tr.CloseIdleConnections()
	client := &http.Client{
		Transport: tr,
		Timeout:   10 * time.Second,
	}
	resp, err := client.Do(req)
	if err != nil {
		msg := err.Error()
		var urlErr *url.Error
		if errors.As(err, &urlErr) && urlErr.Timeout() {
			msg = "Relay test timed out"
		}
		finish(false, msg)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		finish(false, "relay test failed with status "+resp.Status)
		return
	}
	finish(true, "")
}
