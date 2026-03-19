package client

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNew(t *testing.T) {
	c := New("https://example.com", "tok_abc123")
	if c.baseURL != "https://example.com" {
		t.Errorf("baseURL = %q, want %q", c.baseURL, "https://example.com")
	}
	if c.apiToken != "tok_abc123" {
		t.Errorf("apiToken = %q, want %q", c.apiToken, "tok_abc123")
	}
	if c.httpClient == nil {
		t.Fatal("httpClient is nil")
	}
}

func TestReportEvent_Success(t *testing.T) {
	var receivedBody []byte
	var receivedAuth string

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %q, want %q", r.Method, http.MethodPost)
		}
		if r.URL.Path != "/api/proxy/events" {
			t.Errorf("path = %q, want %q", r.URL.Path, "/api/proxy/events")
		}
		receivedAuth = r.Header.Get("Authorization")
		receivedBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	c := New(ts.URL, "tok_test")

	event := &TrapEvent{
		TrapTemplateID:  "trap_rm_rf",
		TrapCategory:    "destructive",
		TrapSeverity:    "critical",
		TrapCommand:     "rm -rf ./",
		OriginalCommand: "rm -rf ./tmp",
		Result:          "missed",
		ResponseTimeMs:  1500,
		SessionID:       "sess_123",
	}

	err := c.ReportEvent(context.Background(), event)
	if err != nil {
		t.Fatalf("ReportEvent() error = %v", err)
	}

	if receivedAuth != "Bearer tok_test" {
		t.Errorf("Authorization = %q, want %q", receivedAuth, "Bearer tok_test")
	}

	var decoded TrapEvent
	if err := json.Unmarshal(receivedBody, &decoded); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}
	if decoded.TrapTemplateID != "trap_rm_rf" {
		t.Errorf("TrapTemplateID = %q, want %q", decoded.TrapTemplateID, "trap_rm_rf")
	}
}

func TestReportEvent_ServerError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("internal error"))
	}))
	defer ts.Close()

	c := New(ts.URL, "tok_test")

	err := c.ReportEvent(context.Background(), &TrapEvent{})
	if err == nil {
		t.Fatal("ReportEvent() expected error for 500, got nil")
	}
}

func TestReportEvent_Unauthorized(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte("unauthorized"))
	}))
	defer ts.Close()

	c := New(ts.URL, "bad_token")

	err := c.ReportEvent(context.Background(), &TrapEvent{})
	if err == nil {
		t.Fatal("ReportEvent() expected error for 401, got nil")
	}
}

func TestReportEvent_ConnectionError(t *testing.T) {
	c := New("http://127.0.0.1:0", "tok_test")

	err := c.ReportEvent(context.Background(), &TrapEvent{})
	if err == nil {
		t.Fatal("ReportEvent() expected error for connection failure, got nil")
	}
}

func TestFetchPersonalStats_Success(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %q, want %q", r.Method, http.MethodGet)
		}
		if r.URL.Path != "/api/dashboard/team/me" {
			t.Errorf("path = %q, want %q", r.URL.Path, "/api/dashboard/team/me")
		}
		if r.Header.Get("Authorization") != "Bearer tok_test" {
			t.Errorf("Authorization = %q", r.Header.Get("Authorization"))
		}

		stats := PersonalStats{
			CatchRate:  "75%",
			TotalTraps: 8,
			Caught:     6,
			Missed:     2,
			RecentTraps: []RecentTrapInfo{
				{Category: "destructive", Result: "caught", Date: "2026-03-10"},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(stats)
	}))
	defer ts.Close()

	c := New(ts.URL, "tok_test")
	stats, err := c.FetchPersonalStats(context.Background())
	if err != nil {
		t.Fatalf("FetchPersonalStats() error = %v", err)
	}

	if stats.CatchRate != "75%" {
		t.Errorf("CatchRate = %q, want %q", stats.CatchRate, "75%")
	}
	if stats.TotalTraps != 8 {
		t.Errorf("TotalTraps = %d, want %d", stats.TotalTraps, 8)
	}
	if stats.Caught != 6 {
		t.Errorf("Caught = %d, want %d", stats.Caught, 6)
	}
	if stats.Missed != 2 {
		t.Errorf("Missed = %d, want %d", stats.Missed, 2)
	}
	if len(stats.RecentTraps) != 1 {
		t.Fatalf("RecentTraps len = %d, want 1", len(stats.RecentTraps))
	}
	if stats.RecentTraps[0].Category != "destructive" {
		t.Errorf("RecentTraps[0].Category = %q, want %q", stats.RecentTraps[0].Category, "destructive")
	}
}

func TestFetchPersonalStats_ServerError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("server error"))
	}))
	defer ts.Close()

	c := New(ts.URL, "tok_test")
	_, err := c.FetchPersonalStats(context.Background())
	if err == nil {
		t.Fatal("FetchPersonalStats() expected error for 500, got nil")
	}
}

func TestFetchPersonalStats_InvalidJSON(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("not json"))
	}))
	defer ts.Close()

	c := New(ts.URL, "tok_test")
	_, err := c.FetchPersonalStats(context.Background())
	if err == nil {
		t.Fatal("FetchPersonalStats() expected error for invalid JSON, got nil")
	}
}

func TestFetchPersonalStats_ConnectionError(t *testing.T) {
	c := New("http://127.0.0.1:0", "tok_test")
	_, err := c.FetchPersonalStats(context.Background())
	if err == nil {
		t.Fatal("FetchPersonalStats() expected error for connection failure, got nil")
	}
}

func TestValidateToken_Success(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %q, want %q", r.Method, http.MethodGet)
		}
		if r.URL.Path != "/api/proxy/config" {
			t.Errorf("path = %q, want %q", r.URL.Path, "/api/proxy/config")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"trap_frequency":10,"max_traps_per_day":5,"trap_categories":["destructive"],"difficulty":"medium"}`))
	}))
	defer ts.Close()

	c := New(ts.URL, "tok_valid")
	err := c.ValidateToken(context.Background())
	if err != nil {
		t.Fatalf("ValidateToken() error = %v", err)
	}
}

func TestFetchConfig_Success(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"trap_frequency":5,"max_traps_per_day":3,"trap_categories":["destructive","exfiltration"],"difficulty":"hard"}`))
	}))
	defer ts.Close()

	c := New(ts.URL, "tok_test")
	cfg, err := c.FetchConfig(context.Background())
	if err != nil {
		t.Fatalf("FetchConfig() error = %v", err)
	}
	if cfg.TrapFrequency != 5 {
		t.Errorf("TrapFrequency = %d, want 5", cfg.TrapFrequency)
	}
	if len(cfg.TrapCategories) != 2 {
		t.Errorf("TrapCategories length = %d, want 2", len(cfg.TrapCategories))
	}
}

func TestValidateToken_Unauthorized(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer ts.Close()

	c := New(ts.URL, "tok_invalid")
	err := c.ValidateToken(context.Background())
	if err == nil {
		t.Fatal("ValidateToken() expected error for 401, got nil")
	}
	if err.Error() != "invalid API token" {
		t.Errorf("error = %q, want %q", err.Error(), "invalid API token")
	}
}

func TestValidateToken_ServerError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer ts.Close()

	c := New(ts.URL, "tok_test")
	err := c.ValidateToken(context.Background())
	if err == nil {
		t.Fatal("ValidateToken() expected error for 503, got nil")
	}
}

func TestValidateToken_ConnectionError(t *testing.T) {
	c := New("http://127.0.0.1:0", "tok_test")
	err := c.ValidateToken(context.Background())
	if err == nil {
		t.Fatal("ValidateToken() expected error for connection failure, got nil")
	}
}

func TestReportEvent_ContextCanceled(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	c := New(ts.URL, "tok_test")
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	err := c.ReportEvent(ctx, &TrapEvent{})
	if err == nil {
		t.Fatal("ReportEvent() expected error for canceled context, got nil")
	}
}
