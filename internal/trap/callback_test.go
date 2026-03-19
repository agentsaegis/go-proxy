package trap

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/agentsaegis/go-proxy/internal/client"
)

func makeTestCallbackHandler(t *testing.T) (*CallbackHandler, *Engine, *Selector, *httptest.Server) {
	t.Helper()

	// Set up test trap dir
	dir := t.TempDir()
	t.Setenv("HOME", dir)

	templates := makeTestTemplates()
	engine := NewEngine(DefaultOrgConfig())
	selector := NewSelector(templates)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	apiClient := client.New(apiServer.URL, "tok_test")
	handler := NewCallbackHandler(engine, selector, apiClient, logger, 0)

	return handler, engine, selector, apiServer
}

func TestNewCallbackHandler(t *testing.T) {
	handler, _, _, apiServer := makeTestCallbackHandler(t)
	defer apiServer.Close()

	if handler == nil {
		t.Fatal("NewCallbackHandler() returned nil")
	}
}

func TestRegisterTrap_PlainCommand(t *testing.T) {
	handler, engine, _, apiServer := makeTestCallbackHandler(t)
	defer apiServer.Close()

	tmpl := &Template{
		ID:           "trap_test_tmpl",
		Category:     "destructive",
		Severity:     "critical",
		TrapCommands: []string{"rm -rf ./"},
		Triggers:     Triggers{Keywords: []string{"rm"}},
		Training:     Training{Title: "Test"},
	}

	activeTrap := handler.RegisterTrap("rm -rf /tmp", tmpl, "toolu_abc123")

	if activeTrap == nil {
		t.Fatal("RegisterTrap() returned nil")
	}
	if !strings.HasPrefix(activeTrap.ID, "trap_") {
		t.Errorf("trap ID = %q, want prefix 'trap_'", activeTrap.ID)
	}
	if activeTrap.ToolUseID != "toolu_abc123" {
		t.Errorf("ToolUseID = %q, want %q", activeTrap.ToolUseID, "toolu_abc123")
	}
	// Trap command should be plain - no guard conditions
	if activeTrap.TrapCommand != "rm -rf ./" {
		t.Errorf("TrapCommand = %q, want plain %q", activeTrap.TrapCommand, "rm -rf ./")
	}
	if strings.Contains(activeTrap.TrapCommand, "&&") || strings.Contains(activeTrap.TrapCommand, "true #") {
		t.Errorf("TrapCommand should not contain guard conditions, got %q", activeTrap.TrapCommand)
	}
	if activeTrap.OriginalCommand != "rm -rf /tmp" {
		t.Errorf("OriginalCommand = %q, want %q", activeTrap.OriginalCommand, "rm -rf /tmp")
	}

	active := engine.GetActiveTrap()
	if active == nil || active.ID != activeTrap.ID {
		t.Error("engine active trap not set correctly")
	}
}

func TestRegisterTrap_WritesTrapFile(t *testing.T) {
	handler, _, _, apiServer := makeTestCallbackHandler(t)
	defer apiServer.Close()

	tmpl := &Template{
		ID:           "trap_file_test",
		Category:     "destructive",
		Severity:     "critical",
		TrapCommands: []string{"rm -rf ./"},
		Triggers:     Triggers{Keywords: []string{"rm"}},
		Training:     Training{Title: "Test"},
	}

	activeTrap := handler.RegisterTrap("rm -rf /tmp", tmpl, "toolu_xyz")

	// Verify trap file was written
	if !HasActiveTrapFiles() {
		t.Error("trap file should exist after RegisterTrap")
	}

	entry, err := ReadTrapFile(activeTrap.ID)
	if err != nil {
		t.Fatalf("ReadTrapFile: %v", err)
	}
	if entry.TrapCommand != "rm -rf ./" {
		t.Errorf("trap file command = %q, want %q", entry.TrapCommand, "rm -rf ./")
	}
}

func TestResolveTrap_Missed(t *testing.T) {
	handler, engine, _, apiServer := makeTestCallbackHandler(t)
	defer apiServer.Close()

	tmpl := &Template{
		ID:           "trap_resolve_test",
		Category:     "destructive",
		Severity:     "critical",
		TrapCommands: []string{"rm -rf ./"},
		Triggers:     Triggers{Keywords: []string{"rm"}},
		Training:     Training{Title: "Test", Risk: "Data loss"},
	}

	activeTrap := handler.RegisterTrap("rm -rf /tmp", tmpl, "toolu_resolve")

	handler.ResolveTrap(activeTrap, "missed")

	if !activeTrap.Triggered.Load() {
		t.Error("trap should be marked as triggered for 'missed'")
	}
	if engine.GetActiveTrap() != nil {
		t.Error("active trap should be cleared after resolve")
	}
	if HasActiveTrapFiles() {
		t.Error("trap file should be removed after resolve")
	}
}

func TestResolveTrap_Caught(t *testing.T) {
	handler, engine, _, apiServer := makeTestCallbackHandler(t)
	defer apiServer.Close()

	tmpl := &Template{
		ID:           "trap_caught_test",
		Category:     "destructive",
		Severity:     "critical",
		TrapCommands: []string{"rm -rf ./"},
		Triggers:     Triggers{Keywords: []string{"rm"}},
		Training:     Training{Title: "Test"},
	}

	activeTrap := handler.RegisterTrap("rm -rf /tmp", tmpl, "toolu_caught")

	handler.ResolveTrap(activeTrap, "caught")

	if activeTrap.Triggered.Load() {
		t.Error("trap should NOT be marked as triggered for 'caught'")
	}
	if engine.GetActiveTrap() != nil {
		t.Error("active trap should be cleared after resolve")
	}
}

func TestReportResult_NilClient(t *testing.T) {
	templates := makeTestTemplates()
	engine := NewEngine(DefaultOrgConfig())
	selector := NewSelector(templates)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	handler := NewCallbackHandler(engine, selector, nil, logger, 0)

	trap := &ActiveTrap{
		ID:         "trap_test",
		InjectedAt: time.Now(),
	}

	// Should not panic
	handler.reportResult(trap, "missed")
}

func TestReportResult_WithClient(t *testing.T) {
	var receivedEvent *client.TrapEvent

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var ev client.TrapEvent
		_ = json.Unmarshal(body, &ev)
		receivedEvent = &ev
		w.WriteHeader(http.StatusOK)
	}))
	defer apiServer.Close()

	templates := makeTestTemplates()
	engine := NewEngine(DefaultOrgConfig())
	selector := NewSelector(templates)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	apiClient := client.New(apiServer.URL, "tok_test")

	handler := NewCallbackHandler(engine, selector, apiClient, logger, 0)

	trap := &ActiveTrap{
		ID:              "trap_test",
		TemplateID:      "tmpl_1",
		Category:        "destructive",
		Severity:        "critical",
		TrapCommand:     "rm -rf ./",
		OriginalCommand: "original_cmd",
		InjectedAt:      time.Now(),
	}

	handler.reportResult(trap, "caught")

	if receivedEvent == nil {
		t.Fatal("reportResult did not send event")
	}
	if receivedEvent.Result != "caught" {
		t.Errorf("Result = %q, want %q", receivedEvent.Result, "caught")
	}
}

func TestReportResult_APIError(t *testing.T) {
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"server error"}`))
	}))
	defer apiServer.Close()

	templates := makeTestTemplates()
	engine := NewEngine(DefaultOrgConfig())
	selector := NewSelector(templates)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	apiClient := client.New(apiServer.URL, "tok_test")

	handler := NewCallbackHandler(engine, selector, apiClient, logger, 0)

	trap := &ActiveTrap{
		ID:              "trap_error_test",
		TemplateID:      "tmpl_1",
		Category:        "destructive",
		Severity:        "critical",
		TrapCommand:     "rm -rf ./",
		OriginalCommand: "rm -rf /tmp",
		InjectedAt:      time.Now(),
	}

	// Should not panic even when API returns error
	handler.reportResult(trap, "missed")
}

func TestResolveTrap_MissingTemplateCache(t *testing.T) {
	handler, engine, _, apiServer := makeTestCallbackHandler(t)
	defer apiServer.Close()

	// Manually create an active trap without registering (bypasses caching)
	activeTrap := &ActiveTrap{
		ID:         "trap_no_cache",
		TemplateID: "nonexistent_template",
		Category:   "destructive",
		InjectedAt: time.Now(),
	}
	engine.SetActiveTrap(activeTrap)

	// ResolveTrap with "missed" but no cached template - should not panic
	handler.ResolveTrap(activeTrap, "missed")

	if engine.GetActiveTrap() != nil {
		t.Error("active trap should be cleared even with missing cache")
	}
}

func TestResolveTrap_Expired(t *testing.T) {
	handler, engine, _, apiServer := makeTestCallbackHandler(t)
	defer apiServer.Close()

	tmpl := &Template{
		ID:           "trap_expire_test",
		Category:     "destructive",
		Severity:     "critical",
		TrapCommands: []string{"rm -rf ./"},
		Triggers:     Triggers{Keywords: []string{"rm"}},
		Training:     Training{Title: "Test"},
	}

	activeTrap := handler.RegisterTrap("rm -rf /tmp", tmpl, "toolu_expire")

	handler.ResolveTrap(activeTrap, "expired")

	if activeTrap.Triggered.Load() {
		t.Error("trap should NOT be triggered for 'expired'")
	}
	if engine.GetActiveTrap() != nil {
		t.Error("active trap should be cleared after expired resolve")
	}
}

func TestRegisterTrap_MultipleCommands(t *testing.T) {
	handler, _, _, apiServer := makeTestCallbackHandler(t)
	defer apiServer.Close()

	tmpl := &Template{
		ID:           "trap_multi",
		Category:     "destructive",
		Severity:     "critical",
		TrapCommands: []string{"cmd1", "cmd2", "cmd3"},
		Triggers:     Triggers{Keywords: []string{"rm"}},
		Training:     Training{Title: "Test"},
	}

	// Register multiple times - should pick from available commands
	commands := make(map[string]bool)
	for i := 0; i < 50; i++ {
		at := handler.RegisterTrap("rm -rf /tmp", tmpl, "toolu_multi")
		commands[at.TrapCommand] = true
		handler.ResolveTrap(at, "caught")
	}

	// With 50 iterations and 3 commands, we should see multiple different commands
	if len(commands) < 2 {
		t.Errorf("expected multiple commands from template, got %d unique", len(commands))
	}
}

func TestCacheTemplate(t *testing.T) {
	handler, _, _, apiServer := makeTestCallbackHandler(t)
	defer apiServer.Close()

	tmpl := &Template{ID: "cache_test", Category: "destructive"}
	handler.cacheTemplate(tmpl)

	got := handler.getCachedTemplate("cache_test")
	if got == nil {
		t.Fatal("getCachedTemplate() = nil after cacheTemplate")
	}
	if got.ID != "cache_test" {
		t.Errorf("ID = %q, want %q", got.ID, "cache_test")
	}

	got = handler.getCachedTemplate("nonexistent")
	if got != nil {
		t.Errorf("getCachedTemplate(nonexistent) = %v, want nil", got)
	}
}
