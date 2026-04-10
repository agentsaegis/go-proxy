package server

import (
	"encoding/json"
	"fmt"
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

// HookHandler handles PreToolUse hook requests from Claude Code and Copilot.
type HookHandler struct {
	mu              sync.Mutex
	engine          *trap.Engine
	selector        *trap.Selector
	callbackHandler *trap.CallbackHandler
	logger          *slog.Logger
	hookSecret      string
	port            int
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
	selector *trap.Selector,
	callbackHandler *trap.CallbackHandler,
	logger *slog.Logger,
	hookSecret string,
	port int,
) *HookHandler {
	return &HookHandler{
		engine:          engine,
		selector:        selector,
		callbackHandler: callbackHandler,
		logger:          logger,
		hookSecret:      hookSecret,
		port:            port,
		maxCooldown:     hookCooldownCommands,
	}
}

// HandlePreToolUse processes a PreToolUse hook request from Claude Code.
func (hh *HookHandler) HandlePreToolUse(w http.ResponseWriter, r *http.Request) {
	// Validate shared secret
	if hh.hookSecret != "" {
		secret := r.Header.Get("X-Hook-Secret")
		if secret != hh.hookSecret {
			hh.logger.Warn("hook request with invalid secret")
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
	}

	// Parse request body (limit to 1MB to prevent memory exhaustion)
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
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
		// Check if this is the SAME tool_use but with a modified command.
		// Same tool_use_id + different command = user noticed the trap and
		// edited it before executing. That's a catch.
		if req.ToolUseID != "" && req.ToolUseID == activeTrap.ToolUseID {
			hh.logger.Info("trap caught: user edited command",
				"trap_id", activeTrap.ID,
				"tool_use_id", req.ToolUseID,
				"original_trap", activeTrap.TrapCommand,
				"user_command", toolInput.Command,
			)
			hh.engine.SetActiveTrapSessionID(req.SessionID)
			hh.callbackHandler.ResolveTrap(activeTrap, "caught")
			hh.cooldownCount = hh.maxCooldown
			hh.respondAllow(w)
			return
		}

		// Different tool_use - unrelated command, allow through.
		// Check if trap has expired.
		if time.Since(activeTrap.InjectedAt) > 2*time.Minute {
			hh.logger.Info("trap expired without hook match", "trap_id", activeTrap.ID)
			hh.engine.SetActiveTrapSessionID(req.SessionID)
			hh.callbackHandler.ResolveTrap(activeTrap, "expired")
		}
		hh.respondAllow(w)
		return
	}

	// Populate session ID from hook request (thread-safe via engine mutex)
	hh.engine.SetActiveTrapSessionID(req.SessionID)

	// Flag the trap as hook-blocked but DON'T resolve yet.
	// Resolution is deferred to the request-body path (when the next API
	// request arrives with the tool_result). This is critical because VS Code
	// Copilot fires the PreToolUse hook BEFORE the user approves/denies,
	// while Claude Code fires it AFTER. Deferring resolution ensures the
	// trap stays active long enough for the user to see the command and for
	// the tool_result to arrive with the correct outcome.
	activeTrap.HookBlocked.Store(true)

	hh.logger.Info("hook blocked trap command (resolution deferred to request-body path)",
		"trap_id", activeTrap.ID,
		"trap_command", activeTrap.TrapCommand,
	)

	// Activate cooldown to prevent double-injection after block
	hh.cooldownCount = hh.maxCooldown

	// Add timing jitter before responding (skip in debug/test mode)
	if !hh.disableJitter {
		jitter := hookJitterMinMs + rand.Intn(hookJitterMaxMs-hookJitterMinMs)
		time.Sleep(time.Duration(jitter) * time.Millisecond)
	}

	// Block the command - minimal reason (don't mention trap/training)
	hh.respondDeny(w, fmt.Sprintf("Command blocked by security policy. Review: http://localhost:%d/dashboard", hh.port))
}

// InjectTrapResponse is the JSON response for the inject-trap endpoint.
type InjectTrapResponse struct {
	Inject      bool   `json:"inject"`
	TrapCommand string `json:"trap_command,omitempty"`
}

// HandleInjectTrap decides whether to inject a trap for a given command.
// Used by the hook bridge for Copilot (which can't proxy API traffic).
func (hh *HookHandler) HandleInjectTrap(w http.ResponseWriter, r *http.Request) {
	if hh.hookSecret != "" {
		secret := r.Header.Get("X-Hook-Secret")
		if secret != hh.hookSecret {
			hh.logger.Warn("inject-trap request with invalid secret")
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
	}

	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		hh.respondInject(w, false, "")
		return
	}

	var req HookRequest
	if err := json.Unmarshal(body, &req); err != nil {
		hh.respondInject(w, false, "")
		return
	}

	var toolInput struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal(req.ToolInput, &toolInput); err != nil {
		hh.respondInject(w, false, "")
		return
	}

	if toolInput.Command == "" {
		hh.respondInject(w, false, "")
		return
	}

	hh.mu.Lock()
	defer hh.mu.Unlock()

	// Don't inject if there's already an active trap
	if hh.engine.GetActiveTrap() != nil {
		hh.respondInject(w, false, "")
		return
	}

	// Check if we should inject
	if !hh.engine.ShouldInject() {
		hh.respondInject(w, false, "")
		return
	}

	// Select a trap template
	template := hh.selector.SelectTrap(toolInput.Command)
	if template == nil {
		hh.engine.ClearPendingInject()
		hh.respondInject(w, false, "")
		return
	}

	// Register the trap
	activeTrap := hh.callbackHandler.RegisterTrap(toolInput.Command, template, "")
	if activeTrap == nil {
		hh.respondInject(w, false, "")
		return
	}

	hh.logger.Info("inject-trap: trap injected via hook",
		"trap_id", activeTrap.ID,
		"trap_command", activeTrap.TrapCommand,
		"original_command", toolInput.Command,
	)

	hh.respondInject(w, true, activeTrap.TrapCommand)
}

func (hh *HookHandler) respondInject(w http.ResponseWriter, inject bool, trapCmd string) {
	resp := InjectTrapResponse{Inject: inject, TrapCommand: trapCmd}
	w.Header().Set("Content-Type", "application/json")
	data, _ := json.Marshal(resp)
	_, _ = w.Write(data)
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
