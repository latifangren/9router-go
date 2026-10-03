package chat

import (
	"context"
	json "encoding/json/v2"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"9router/proxy/internal/db"
	"9router/proxy/internal/providers"
)

// TestFetchAntigravityProjectID_outcomes pins the classification that drives the
// refresh/no-cache decisions: pid found, token rejected, project definitively
// missing vs transient rate limit.
func TestFetchAntigravityProjectID_outcomes(t *testing.T) {
	tests := []struct {
		name        string
		loadAssist  int    // status code for loadCodeAssist
		loadBody    string // body for loadCodeAssist
		onboard     int    // status code for onboardUser
		onboardBody string // body for onboardUser
		wantPID     string
		wantAuth    bool
		wantNoProj  bool
	}{
		{
			name:       "project found",
			loadAssist: 200,
			loadBody:   `{"cloudaicompanionProject":{"id":"proj-123","name":"x"}}`,
			wantPID:    "proj-123",
		},
		{
			name:       "token rejected 401",
			loadAssist: 401,
			wantAuth:   true,
		},
		{
			name:       "token rejected 403",
			loadAssist: 403,
			wantAuth:   true,
		},
		{
			name:        "empty project confirmed",
			loadAssist:  200,
			loadBody:    `{"allowedTiers":[{"id":"standard-tier","isDefault":true}]}`,
			onboard:     200,
			onboardBody: `{"done":true,"response":{"cloudaicompanionProject":{}}}`,
			wantNoProj:  true,
		},
		{
			// loadCodeAssist already said "no project for this token" (200, tiers
			// only) — an onboardUser 429 afterwards doesn't change that verdict,
			// so it's still cached as no-project.
			name:       "onboard rate-limited after clean load",
			loadAssist: 200,
			loadBody:   `{"allowedTiers":[{"id":"standard-tier","isDefault":true}]}`,
			onboard:    429,
			wantNoProj: true,
			wantAuth:   false,
		},
		{
			name:       "concurrent connection refused is transient",
			loadAssist: 503,
			wantNoProj: false,
		},
	}

	oldDelay := antigravityProbeDelay
	antigravityProbeDelay = time.Millisecond
	defer func() { antigravityProbeDelay = oldDelay }()

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.Contains(r.URL.Path, "loadCodeAssist") {
					w.WriteHeader(tc.loadAssist)
					w.Write([]byte(tc.loadBody))
					return
				}
				w.WriteHeader(tc.onboard)
				w.Write([]byte(tc.onboardBody))
			}))
			defer srv.Close()

			oldL, oldO := loadCodeAssistURL, onboardUserURL
			loadCodeAssistURL, onboardUserURL = srv.URL+"/loadCodeAssist", srv.URL+"/onboardUser"
			defer func() { loadCodeAssistURL, onboardUserURL = oldL, oldO }()

			pid, auth, noProj := fetchAntigravityProjectID(context.Background(), srv.Client(), "test-token")
			if pid != tc.wantPID {
				t.Errorf("pid = %q, want %q", pid, tc.wantPID)
			}
			if auth != tc.wantAuth {
				t.Errorf("authFailed = %v, want %v", auth, tc.wantAuth)
			}
			if noProj != tc.wantNoProj {
				t.Errorf("noProject = %v, want %v", noProj, tc.wantNoProj)
			}
		})
	}
}

func TestProjectNoCache(t *testing.T) {
	projectNoCache.Delete("test-conn")
	if projectProbeCached("test-conn") {
		t.Fatal("fresh cache should not report cached")
	}
	cacheProjectMissing("test-conn")
	if !projectProbeCached("test-conn") {
		t.Fatal("should be cached after cacheProjectMissing")
	}
	projectNoCache.Store("test-conn", int64(time.Now().Add(-time.Second).Unix()))
	if projectProbeCached("test-conn") {
		t.Fatal("expired cache entry should not report cached")
	}
}

func TestAntigravityProbe_HeadersAndMetadata(t *testing.T) {
	var gotUA, gotGoogClient, gotClientMeta string
	var gotBodyMeta map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.Header.Get("User-Agent")
		gotGoogClient = r.Header.Get("X-Goog-Api-Client")
		gotClientMeta = r.Header.Get("Client-Metadata")

		var body struct {
			Metadata map[string]any `json:"metadata"`
		}
		_ = json.Unmarshal(mustReadAll(r.Body), &body)
		gotBodyMeta = body.Metadata

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"cloudaicompanionProject":{"id":"test-proj"}}`))
	}))
	defer srv.Close()

	oldL, oldO := loadCodeAssistURL, onboardUserURL
	loadCodeAssistURL, onboardUserURL = srv.URL+"/loadCodeAssist", srv.URL+"/onboardUser"
	defer func() { loadCodeAssistURL, onboardUserURL = oldL, oldO }()

	pid, auth, noProj := fetchAntigravityProjectID(context.Background(), srv.Client(), "test-token")
	if pid != "test-proj" || auth || noProj {
		t.Fatalf("unexpected result: pid=%q, auth=%v, noProj=%v", pid, auth, noProj)
	}

	if gotUA != "antigravity/ide/2.11.0 darwin/arm64" {
		t.Errorf("User-Agent = %q, want %q", gotUA, "antigravity/ide/2.11.0 darwin/arm64")
	}
	if gotGoogClient != "gl-node/22.21.1" {
		t.Errorf("X-Goog-Api-Client = %q, want %q", gotGoogClient, "gl-node/22.21.1")
	}
	if !strings.Contains(gotClientMeta, `"ideType":"ANTIGRAVITY"`) {
		t.Errorf("Client-Metadata = %q, want it to contain ideType ANTIGRAVITY", gotClientMeta)
	}
	if gotBodyMeta["ideType"] != "ANTIGRAVITY" {
		t.Errorf("body metadata ideType = %v, want ANTIGRAVITY", gotBodyMeta["ideType"])
	}
	// Verify old metadata fields are removed
	if _, ok := gotBodyMeta["pluginType"]; ok {
		t.Errorf("body metadata unexpectedly contains pluginType: %v", gotBodyMeta["pluginType"])
	}
	if _, ok := gotBodyMeta["platform"]; ok {
		t.Errorf("body metadata unexpectedly contains platform: %v", gotBodyMeta["platform"])
	}
}

func TestExtractProjectID(t *testing.T) {
	cases := []struct {
		input any
		want  string
	}{
		{nil, ""},
		{"", ""},
		{"  ", ""},
		{"proj-direct", "proj-direct"},
		{"  proj-trimmed  ", "proj-trimmed"},
		{map[string]any{"id": "proj-from-id"}, "proj-from-id"},
		{map[string]any{"projectId": "proj-from-projectId"}, "proj-from-projectId"},
		{map[string]any{"project_id": "proj-from-project_id"}, "proj-from-project_id"},
		{map[string]any{"id": "", "projectId": "fallback-pid"}, "fallback-pid"},
		{map[string]any{}, ""},
		{123, ""},
	}

	for _, tc := range cases {
		got := extractProjectID(tc.input)
		if got != tc.want {
			t.Errorf("extractProjectID(%v) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestResolveAntigravityProjectID(t *testing.T) {
	// Case 1: Probed project found
	srvFound := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"cloudaicompanionProject":{"id":"probed-123"}}`))
	}))
	defer srvFound.Close()

	oldL, oldO := loadCodeAssistURL, onboardUserURL
	loadCodeAssistURL, onboardUserURL = srvFound.URL+"/loadCodeAssist", srvFound.URL+"/onboardUser"
	pid := ResolveAntigravityProjectID(context.Background(), srvFound.Client(), "token")
	if pid != "probed-123" {
		t.Errorf("ResolveAntigravityProjectID found = %q, want probed-123", pid)
	}

	// Case 2: Probed 401 Unauthorized -> falls back to aicode-consumers
	srv401 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
	}))
	defer srv401.Close()

	loadCodeAssistURL, onboardUserURL = srv401.URL+"/loadCodeAssist", srv401.URL+"/onboardUser"
	pidFallback := ResolveAntigravityProjectID(context.Background(), srv401.Client(), "token")
	if pidFallback != DefaultAntigravityProjectID {
		t.Errorf("ResolveAntigravityProjectID on 401 = %q, want %q", pidFallback, DefaultAntigravityProjectID)
	}

	loadCodeAssistURL, onboardUserURL = oldL, oldO
}

func TestForwardGeminiNativeRequest_FallbackToAICodeConsumers(t *testing.T) {
	var receivedProject string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "loadCodeAssist") {
			// Simulate 401 error during project probing
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
			return
		}

		body, _ := io.ReadAll(r.Body)
		var req struct {
			Project string `json:"project"`
		}
		_ = json.Unmarshal(body, &req)
		receivedProject = req.Project

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"response":{"candidates":[{"content":{"parts":[{"text":"pong"}]}}]}}`))
	}))
	defer srv.Close()

	oldL, oldO := loadCodeAssistURL, onboardUserURL
	loadCodeAssistURL, onboardUserURL = srv.URL+"/loadCodeAssist", srv.URL+"/onboardUser"
	defer func() { loadCodeAssistURL, onboardUserURL = oldL, oldO }()

	h := NewChatHandler(nil)
	cfg := &providers.ProviderConfig{
		BaseURL: srv.URL,
	}

	body := []byte(`{"model":"gemini-3.8-flash-high","messages":[{"role":"user","content":"ping"}]}`)
	rec := httptest.NewRecorder()

	err := h.forwardGeminiNativeRequest(
		context.Background(),
		rec,
		"antigravity",
		cfg,
		"mock-api-key",
		"",
		body,
		false,
		false,
		nil,
		srv.Client(),
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if receivedProject != DefaultAntigravityProjectID {
		t.Errorf("expected receivedProject %q, got %q", DefaultAntigravityProjectID, receivedProject)
	}
}

func TestForwardGeminiNativeRequest_PersistsAICodeConsumersToConnection(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()

	connID := "ag-test-conn-123"
	initData := `{"accessToken":"valid-token","refreshToken":"rt"}`
	_, err := database.Exec(
		`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt)
		 VALUES (?, 'antigravity', 'oauth', 'AG Test', 1, 1, ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`,
		connID, initData,
	)
	if err != nil {
		t.Fatalf("insert connection failed: %v", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "loadCodeAssist") {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"response":{"candidates":[{"content":{"parts":[{"text":"ok"}]}}]}}`))
	}))
	defer srv.Close()

	oldL, oldO := loadCodeAssistURL, onboardUserURL
	loadCodeAssistURL, onboardUserURL = srv.URL+"/loadCodeAssist", srv.URL+"/onboardUser"
	defer func() { loadCodeAssistURL, onboardUserURL = oldL, oldO }()

	handler := NewChatHandler(db.NewRepo(database))
	cfg := &providers.ProviderConfig{
		BaseURL: srv.URL,
	}

	body := []byte(`{"model":"gemini-3.8-flash-high","messages":[{"role":"user","content":"hi"}]}`)
	rec := httptest.NewRecorder()

	err = handler.forwardGeminiNativeRequest(
		context.Background(),
		rec,
		"antigravity",
		cfg,
		"valid-token",
		connID,
		body,
		false,
		false,
		nil,
		srv.Client(),
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Give the goroutine in storeAntigravityProjectID time to commit
	var savedData string
	for i := 0; i < 20; i++ {
		time.Sleep(10 * time.Millisecond)
		_ = database.QueryRow("SELECT data FROM providerConnections WHERE id = ?", connID).Scan(&savedData)
		if strings.Contains(savedData, "aicode-consumers") {
			break
		}
	}

	var dMap map[string]any
	if err := json.Unmarshal([]byte(savedData), &dMap); err != nil {
		t.Fatalf("unmarshal saved data: %v", err)
	}
	if dMap["projectId"] != DefaultAntigravityProjectID {
		t.Errorf("expected stored projectId %q, got %v", DefaultAntigravityProjectID, dMap["projectId"])
	}
}

func mustReadAll(r io.Reader) []byte {
	b, _ := io.ReadAll(r)
	return b
}
