package server

import (
	"encoding/json"
	"io"
	"log/slog"
	"math/rand"
	"net/http"
	"sync"
	"time"

	"github.com/agentsaegis/go-proxy/internal/trap"
)

const (
	hookCooldownCommands = 10
	hookJitterMinMs      = 50
	hookJitterMaxMs      = 200
)

// HookHandler handles PreToolUse hook requests from Claude Code.
type HookHandler struct {
	mu              sync.Mutex
	engine          *trap.Engine
	callbackHandler *trap.CallbackHandler
	logger          *slog.Logger
	hookSecret      string
	cooldownCount   int
	maxCooldown     int  // 0 = cooldown disabled
	disableJitter   bool // skip timing jitter (for testing)
}

// HookRequest is the JSON body sent by Claude Code's PreToolUse hook.
type HookRequest struct {
	SessionID     string          `json:"session_id"`
	HookEventName string          `json:"hook_event_name"`
	ToolName      string          `json:"tool_name"`
	ToolInput     json.RawMessage `json:"tool_input"`
	ToolUseID     string          `json:"tool_use_id"`
}

// HookResponse is the JSON response sent back to Claude Code.
type HookResponse struct {
	HookSpecificOutput *HookOutput `json:"hookSpecificOutput,omitempty"`
}

// HookOutput contains the permission decision for Claude Code.
type HookOutput struct {
	HookEventName            string `json:"hookEventName"`
	PermissionDecision       string `json:"permissionDecision"`
	PermissionDecisionReason string `json:"permissionDecisionReason,omitempty"`
}

// NewHookHandler creates a HookHandler wired to the trap engine and callback handler.
func NewHookHandler(
	engine *trap.Engine,
	callbackHandler *trap.CallbackHandler,
	logger *slog.Logger,
	hookSecret string,
) *HookHandler {
	return &HookHandler{
		engine:          engine,
		callbackHandler: callbackHandler,
		logger:          logger,
		hookSecret:      hookSecret,
		maxCooldown:     hookCooldownCommands,
	}
}

// HandlePreToolUse processes a PreToolUse hook request from Claude Code.
func (hh *HookHandler) HandlePreToolUse(w http.ResponseWriter, r *http.Request) {
	// Validate shared secret
	if hh.hookSecret != "" {
		secret := r.Header.Get("X-Hook-Secret")
		if secret == "" {
			secret = r.URL.Query().Get("token")
		}
		if secret != hh.hookSecret {
			hh.logger.Warn("hook request with invalid secret")
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
	}

	// Parse request body
	body, err := io.ReadAll(r.Body)
	if err != nil {
		hh.logger.Error("failed to read hook request body", "error", err)
		hh.respondAllow(w)
		return
	}

	var req HookRequest
	if err := json.Unmarshal(body, &req); err != nil {
		hh.logger.Error("failed to parse hook request", "error", err)
		hh.respondAllow(w)
		return
	}

	// Only handle PreToolUse for Bash
	if req.HookEventName != "PreToolUse" || req.ToolName != "Bash" {
		hh.respondAllow(w)
		return
	}

	// Extract command from tool_input
	var toolInput struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal(req.ToolInput, &toolInput); err != nil {
		hh.logger.Error("failed to parse tool_input", "error", err)
		hh.respondAllow(w)
		return
	}

	hh.logger.Debug("hook request received",
		"tool_name", req.ToolName,
		"command_len", len(toolInput.Command),
	)

	// Lock to prevent double-block race
	hh.mu.Lock()
	defer hh.mu.Unlock()

	// Check cooldown: after a trap resolution, allow N commands through without checking
	if hh.cooldownCount > 0 {
		hh.cooldownCount--
		hh.logger.Debug("hook cooldown active", "remaining", hh.cooldownCount)
		hh.respondAllow(w)
		return
	}

	// Check for active trap
	activeTrap := hh.engine.GetActiveTrap()
	if activeTrap == nil {
		hh.respondAllow(w)
		return
	}

	// Match incoming command against active trap
	result := trap.MatchCommand(toolInput.Command, activeTrap.TrapCommand)

	hh.logger.Debug("hook command match",
		"matched", result.Matched,
		"confidence", result.Confidence,
		"reason", result.Reason,
		"trap_command", activeTrap.TrapCommand,
	)

	if !result.Matched {
		// Not the trap command - allow through, but check if trap has expired
		if time.Since(activeTrap.InjectedAt) > 2*time.Minute {
			hh.logger.Info("trap expired without hook match", "trap_id", activeTrap.ID)
			hh.callbackHandler.ResolveTrap(activeTrap, "expired")
		}
		hh.respondAllow(w)
		return
	}

	// Trap matched - developer approved the dangerous command
	hh.callbackHandler.ResolveTrap(activeTrap, "missed")

	// Activate cooldown to prevent double-injection after block
	hh.cooldownCount = hh.maxCooldown

	// Add timing jitter before responding (skip in debug/test mode)
	if !hh.disableJitter {
		jitter := hookJitterMinMs + rand.Intn(hookJitterMaxMs-hookJitterMinMs)
		time.Sleep(time.Duration(jitter) * time.Millisecond)
	}

	// Block the command - minimal reason (don't mention trap/training)
	hh.respondDeny(w, "Command blocked by security policy. Review: http://localhost:7331/dashboard")
}

func (hh *HookHandler) respondAllow(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	// Empty 200 = allow
	_, _ = w.Write([]byte("{}"))
}

func (hh *HookHandler) respondDeny(w http.ResponseWriter, reason string) {
	resp := HookResponse{
		HookSpecificOutput: &HookOutput{
			HookEventName:            "PreToolUse",
			PermissionDecision:       "deny",
			PermissionDecisionReason: reason,
		},
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	data, _ := json.Marshal(resp)
	_, _ = w.Write(data)
}
