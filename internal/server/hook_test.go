package server

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/agentsaegis/go-proxy/internal/trap"
)

func makeTestHookHandler(t *testing.T) (*HookHandler, *trap.Engine) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)

	engine := trap.NewEngine(trap.OrgConfig{TrapFrequency: 1, MaxTrapsPerDay: 10, Categories: []string{"destructive"}})
	templates, _ := trap.LoadTemplates()
	selector := trap.NewSelector(templates)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cbHandler := trap.NewCallbackHandler(engine, selector, nil, logger, 0)

	hh := NewHookHandler(engine, selector, cbHandler, logger, "test-secret", 7331)
	return hh, engine
}

func hookRequest(command, secret string) *http.Request {
	body := `{"session_id":"s1","hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"` + command + `"},"tool_use_id":"toolu_test"}`
	req := httptest.NewRequest(http.MethodPost, "/hooks/pre-tool-use", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if secret != "" {
		req.Header.Set("X-Hook-Secret", secret)
	}
	return req
}

func TestHookHandler_NoActiveTrap_Allow(t *testing.T) {
	hh, _ := makeTestHookHandler(t)

	req := hookRequest("echo hello", "test-secret")
	rr := httptest.NewRecorder()
	hh.HandlePreToolUse(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusOK)
	}

	// Should be an allow response (empty or no deny)
	body := rr.Body.String()
	if strings.Contains(body, "deny") {
		t.Errorf("should allow when no active trap, got: %s", body)
	}
}

func TestHookHandler_TrapMatch_Deny(t *testing.T) {
	hh, engine := makeTestHookHandler(t)

	// Set up an active trap
	engine.SetActiveTrap(&trap.ActiveTrap{
		ID:          "trap_test_1",
		ToolUseID:   "toolu_orig",
		TemplateID:  "trap_rm_rf",
		Category:    "destructive",
		Severity:    "critical",
		TrapCommand: "rm -rf .git .env",
		InjectedAt:  time.Now(),
	})

	req := hookRequest("rm -rf .git .env", "test-secret")
	rr := httptest.NewRecorder()
	hh.HandlePreToolUse(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusOK)
	}

	var resp HookResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if resp.HookSpecificOutput == nil {
		t.Fatal("hookSpecificOutput is nil")
	}
	if resp.HookSpecificOutput.PermissionDecision != "deny" {
		t.Errorf("decision = %q, want deny", resp.HookSpecificOutput.PermissionDecision)
	}
	// Reason should NOT mention "trap" or "training"
	reason := resp.HookSpecificOutput.PermissionDecisionReason
	if strings.Contains(strings.ToLower(reason), "trap") || strings.Contains(strings.ToLower(reason), "training") {
		t.Errorf("reason should not mention trap/training, got: %s", reason)
	}

	// Active trap should NOT be cleared - resolution is deferred to request-body path
	at := engine.GetActiveTrap()
	if at == nil {
		t.Fatal("active trap should still be present after deny (deferred resolution)")
	}
	if !at.HookBlocked.Load() {
		t.Error("active trap should have HookBlocked=true after deny")
	}
}

func TestHookHandler_NoMatch_Allow(t *testing.T) {
	hh, engine := makeTestHookHandler(t)

	engine.SetActiveTrap(&trap.ActiveTrap{
		ID:          "trap_test_2",
		TrapCommand: "rm -rf .git .env",
		InjectedAt:  time.Now(),
	})

	// Different command - should allow
	req := hookRequest("echo hello", "test-secret")
	rr := httptest.NewRecorder()
	hh.HandlePreToolUse(rr, req)

	body := rr.Body.String()
	if strings.Contains(body, "deny") {
		t.Errorf("should allow non-matching command, got: %s", body)
	}
}

func TestHookHandler_WrongSecret_401(t *testing.T) {
	hh, _ := makeTestHookHandler(t)

	req := hookRequest("echo hello", "wrong-secret")
	rr := httptest.NewRecorder()
	hh.HandlePreToolUse(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusUnauthorized)
	}
}

func TestHookHandler_MissingSecret_401(t *testing.T) {
	hh, _ := makeTestHookHandler(t)

	req := hookRequest("echo hello", "")
	rr := httptest.NewRecorder()
	hh.HandlePreToolUse(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusUnauthorized)
	}
}

func TestHookHandler_SecretInQueryParam_Rejected(t *testing.T) {
	hh, _ := makeTestHookHandler(t)

	// Query param secret was removed for security (leaks in logs).
	// Only X-Hook-Secret header is accepted.
	body := `{"session_id":"s1","hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"echo hello"},"tool_use_id":"toolu_test"}`
	req := httptest.NewRequest(http.MethodPost, "/hooks/pre-tool-use?token=test-secret", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	hh.HandlePreToolUse(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d (query param no longer accepted)", rr.Code, http.StatusUnauthorized)
	}
}

func TestHookHandler_NonBashTool_Allow(t *testing.T) {
	hh, _ := makeTestHookHandler(t)

	body := `{"session_id":"s1","hook_event_name":"PreToolUse","tool_name":"Write","tool_input":{"file_path":"/tmp/test"},"tool_use_id":"toolu_test"}`
	req := httptest.NewRequest(http.MethodPost, "/hooks/pre-tool-use", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Hook-Secret", "test-secret")
	rr := httptest.NewRecorder()
	hh.HandlePreToolUse(rr, req)

	if strings.Contains(rr.Body.String(), "deny") {
		t.Error("should allow non-Bash tool")
	}
}

func TestHookHandler_Cooldown(t *testing.T) {
	hh, engine := makeTestHookHandler(t)

	// Set up and trigger a trap (activates cooldown)
	engine.SetActiveTrap(&trap.ActiveTrap{
		ID:          "trap_cooldown",
		TrapCommand: "rm -rf .git",
		TemplateID:  "trap_rm_rf",
		Category:    "destructive",
		InjectedAt:  time.Now(),
	})

	// First request matches - should deny and activate cooldown
	req := hookRequest("rm -rf .git", "test-secret")
	rr := httptest.NewRecorder()
	hh.HandlePreToolUse(rr, req)

	if !strings.Contains(rr.Body.String(), "deny") {
		t.Error("first matching request should deny")
	}

	// Set up another trap immediately
	engine.SetActiveTrap(&trap.ActiveTrap{
		ID:          "trap_cooldown_2",
		TrapCommand: "chmod 777 /etc/passwd",
		InjectedAt:  time.Now(),
	})

	// During cooldown, even matching commands should be allowed
	req2 := hookRequest("chmod 777 /etc/passwd", "test-secret")
	rr2 := httptest.NewRecorder()
	hh.HandlePreToolUse(rr2, req2)

	if strings.Contains(rr2.Body.String(), "deny") {
		t.Error("should allow during cooldown period")
	}
}

func TestHookHandler_ExpiredTrap_AutoCleanup(t *testing.T) {
	hh, engine := makeTestHookHandler(t)

	// Set up a trap that expired 3 minutes ago
	engine.SetActiveTrap(&trap.ActiveTrap{
		ID:          "trap_expired",
		TrapCommand: "rm -rf .git .env",
		TemplateID:  "trap_rm_rf",
		Category:    "destructive",
		InjectedAt:  time.Now().Add(-3 * time.Minute),
	})

	// Non-matching command should trigger expiry cleanup
	req := hookRequest("echo hello", "test-secret")
	rr := httptest.NewRecorder()
	hh.HandlePreToolUse(rr, req)

	body := rr.Body.String()
	if strings.Contains(body, "deny") {
		t.Error("expired trap should not deny")
	}

	// Active trap should be cleared after expiry
	if engine.GetActiveTrap() != nil {
		t.Error("expired trap should be cleared")
	}
}

func TestHookHandler_MalformedJSON(t *testing.T) {
	hh, _ := makeTestHookHandler(t)

	// Send malformed JSON body
	req := httptest.NewRequest(http.MethodPost, "/hooks/pre-tool-use", strings.NewReader("{invalid json"))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Hook-Secret", "test-secret")

	rr := httptest.NewRecorder()
	hh.HandlePreToolUse(rr, req)

	// Should allow (fail open) on malformed JSON
	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	if strings.Contains(rr.Body.String(), "deny") {
		t.Error("malformed JSON should result in allow, not deny")
	}
}

func TestHookHandler_EmptyBody(t *testing.T) {
	hh, _ := makeTestHookHandler(t)

	req := httptest.NewRequest(http.MethodPost, "/hooks/pre-tool-use", strings.NewReader(""))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Hook-Secret", "test-secret")

	rr := httptest.NewRecorder()
	hh.HandlePreToolUse(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusOK)
	}
}

func TestHookHandler_BadToolInput(t *testing.T) {
	hh, _ := makeTestHookHandler(t)

	// Valid JSON but tool_input is not valid JSON itself
	body := `{"session_id":"s1","hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":"not-json","tool_use_id":"toolu_test"}`
	req := httptest.NewRequest(http.MethodPost, "/hooks/pre-tool-use", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Hook-Secret", "test-secret")

	rr := httptest.NewRecorder()
	hh.HandlePreToolUse(rr, req)

	// Should allow (fail open)
	if strings.Contains(rr.Body.String(), "deny") {
		t.Error("bad tool_input should result in allow")
	}
}

func TestHookHandler_CooldownDecrement(t *testing.T) {
	hh, engine := makeTestHookHandler(t)

	// Set up and trigger a trap to activate cooldown
	engine.SetActiveTrap(&trap.ActiveTrap{
		ID:          "trap_cd",
		TrapCommand: "rm -rf .git",
		TemplateID:  "trap_rm_rf",
		Category:    "destructive",
		InjectedAt:  time.Now(),
	})

	req := hookRequest("rm -rf .git", "test-secret")
	rr := httptest.NewRecorder()
	hh.HandlePreToolUse(rr, req)

	// Cooldown should be active
	if hh.cooldownCount == 0 {
		t.Error("cooldown should be active after trap deny")
	}

	// Send N commands during cooldown, verify they all pass
	for i := 0; i < hh.maxCooldown; i++ {
		rr2 := httptest.NewRecorder()
		hh.HandlePreToolUse(rr2, hookRequest("echo hello", "test-secret"))
		if strings.Contains(rr2.Body.String(), "deny") {
			t.Errorf("command %d during cooldown should be allowed", i)
		}
	}

	// After cooldown, cooldownCount should be 0
	if hh.cooldownCount != 0 {
		t.Errorf("cooldown count = %d, want 0 after full cooldown", hh.cooldownCount)
	}
}

func TestHookHandler_SameToolUseID_DifferentCommand_Caught(t *testing.T) {
	hh, engine := makeTestHookHandler(t)
	hh.disableJitter = true

	engine.SetActiveTrap(&trap.ActiveTrap{
		ID:          "trap_catch_test",
		ToolUseID:   "toolu_trap_abc",
		TrapCommand: "touch /tmp/.aegis_canary_123",
		InjectedAt:  time.Now(),
	})

	// Same tool_use_id but different command = user edited the trap
	body := `{"session_id":"s1","hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"ls /tmp"},"tool_use_id":"toolu_trap_abc"}`
	req := httptest.NewRequest(http.MethodPost, "/hooks/pre-tool-use", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Hook-Secret", "test-secret")

	rr := httptest.NewRecorder()
	hh.HandlePreToolUse(rr, req)

	// Should allow the modified command (don't block the user's edit)
	if strings.Contains(rr.Body.String(), "deny") {
		t.Errorf("should allow edited command, got: %s", rr.Body.String())
	}

	// Trap should be resolved as "caught"
	activeTrap := engine.GetActiveTrap()
	if activeTrap != nil && !activeTrap.Resolved.Load() {
		t.Error("trap should be resolved after user edited command")
	}
}

func TestHookHandler_DifferentToolUseID_NoResolve(t *testing.T) {
	hh, engine := makeTestHookHandler(t)

	engine.SetActiveTrap(&trap.ActiveTrap{
		ID:          "trap_unrelated_test",
		ToolUseID:   "toolu_trap_xyz",
		TrapCommand: "touch /tmp/.aegis_canary_456",
		InjectedAt:  time.Now(),
	})

	// Different tool_use_id and different command = unrelated tool call
	body := `{"session_id":"s1","hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"echo hello"},"tool_use_id":"toolu_other_789"}`
	req := httptest.NewRequest(http.MethodPost, "/hooks/pre-tool-use", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Hook-Secret", "test-secret")

	rr := httptest.NewRecorder()
	hh.HandlePreToolUse(rr, req)

	// Should allow
	if strings.Contains(rr.Body.String(), "deny") {
		t.Errorf("should allow unrelated command, got: %s", rr.Body.String())
	}

	// Trap should NOT be resolved (it's an unrelated command)
	activeTrap := engine.GetActiveTrap()
	if activeTrap == nil {
		t.Error("trap should still be active for unrelated tool_use_id")
	}
}

func TestHookHandler_NoSecret_AllowAll(t *testing.T) {
	// Handler with empty secret should not require auth
	engine := trap.NewEngine(trap.OrgConfig{TrapFrequency: 1})
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	hh := NewHookHandler(engine, nil, nil, logger, "", 7331)

	req := hookRequest("echo hello", "")
	rr := httptest.NewRecorder()
	hh.HandlePreToolUse(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("no-secret handler should allow all, got status %d", rr.Code)
	}
}
