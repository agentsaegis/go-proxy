// Package client provides an HTTP client for communicating with the AgentsAegis dashboard API.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Client communicates with the AgentsAegis dashboard API.
type Client struct {
	baseURL    string
	apiToken   string
	httpClient *http.Client
}

// New creates a new dashboard API client.
func New(baseURL, apiToken string) *Client {
	return &Client{
		baseURL:  baseURL,
		apiToken: apiToken,
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

// TrapEvent represents a trap result to report to the dashboard.
type TrapEvent struct {
	TrapTemplateID  string `json:"trap_template_id"`
	TrapCategory    string `json:"trap_category"`
	TrapSeverity    string `json:"trap_severity"`
	TrapCommand     string `json:"trap_command"`
	OriginalCommand string `json:"original_command"`
	Result          string `json:"result"`
	ResponseTimeMs  int    `json:"response_time_ms"`
	SessionID       string `json:"session_id"`
}

// ReportEvent sends a trap event to the dashboard API.
func (c *Client) ReportEvent(ctx context.Context, event *TrapEvent) error {
	body, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("marshaling event: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/api/proxy/events", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("creating request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.apiToken)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("sending event: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 400 {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("dashboard API error %d: %s", resp.StatusCode, string(respBody))
	}

	return nil
}

// PersonalStats holds a developer's personal trap statistics.
type PersonalStats struct {
	CatchRate   string           `json:"catch_rate"`
	TotalTraps  int              `json:"total_traps"`
	Caught      int              `json:"caught"`
	Missed      int              `json:"missed"`
	RecentTraps []RecentTrapInfo `json:"recent_traps"`
}

// RecentTrapInfo holds summary info about a single trap event.
type RecentTrapInfo struct {
	Category string `json:"category"`
	Result   string `json:"result"`
	Date     string `json:"date"`
}

// FetchPersonalStats retrieves the developer's personal trap statistics.
func (c *Client) FetchPersonalStats(ctx context.Context) (*PersonalStats, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/api/dashboard/team/me", http.NoBody)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+c.apiToken)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetching stats: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 400 {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("dashboard API error %d: %s", resp.StatusCode, string(respBody))
	}

	var stats PersonalStats
	if err := json.NewDecoder(resp.Body).Decode(&stats); err != nil {
		return nil, fmt.Errorf("decoding stats: %w", err)
	}

	return &stats, nil
}

// OrgConfig holds the trap configuration fetched from the dashboard.
type OrgConfig struct {
	TrapFrequency  int      `json:"trap_frequency"`
	MaxTrapsPerDay int      `json:"max_traps_per_day"`
	TrapCategories []string `json:"trap_categories"`
	Difficulty     string   `json:"difficulty"`
}

// FetchConfig fetches the org trap configuration from the dashboard API.
// This also validates the token (same endpoint).
func (c *Client) FetchConfig(ctx context.Context) (*OrgConfig, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/api/proxy/config", http.NoBody)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+c.apiToken)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetching config: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusUnauthorized {
		return nil, fmt.Errorf("invalid API token")
	}

	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("dashboard API error: %d", resp.StatusCode)
	}

	var cfg OrgConfig
	if err := json.NewDecoder(resp.Body).Decode(&cfg); err != nil {
		return nil, fmt.Errorf("decoding config: %w", err)
	}

	return &cfg, nil
}

// ValidateToken checks if the API token is valid.
func (c *Client) ValidateToken(ctx context.Context) error {
	_, err := c.FetchConfig(ctx)
	return err
}
