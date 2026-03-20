package trap

import (
	"context"
	"fmt"
	"log/slog"
	"math/rand"
	"os"
	"sync"
	"time"

	"github.com/agentsaegis/go-proxy/internal/client"
)

// CallbackHandler manages trap lifecycle: registration, resolution via
// PreToolUse hook, and result reporting.
type CallbackHandler struct {
	engine        *Engine
	selector      *Selector
	apiClient     *client.Client
	logger        *slog.Logger
	templateCache map[string]*Template
	cacheMu       sync.Mutex
}

// NewCallbackHandler creates a CallbackHandler wired to the trap engine, selector,
// and dashboard API client.
func NewCallbackHandler(engine *Engine, selector *Selector, apiClient *client.Client, logger *slog.Logger, _ int) *CallbackHandler {
	return &CallbackHandler{
		engine:        engine,
		selector:      selector,
		apiClient:     apiClient,
		logger:        logger,
		templateCache: make(map[string]*Template),
	}
}

// RegisterTrap creates and registers a new ActiveTrap with a plain (unwrapped)
// trap command from the template. Writes a trap file for the fallback script.
func (h *CallbackHandler) RegisterTrap(originalCmd string, template *Template, toolUseID string) *ActiveTrap {
	trapID := fmt.Sprintf("trap_%d", time.Now().UnixNano())

	// Pick a plain trap command directly from the template - no wrapping
	trapCmd := template.TrapCommands[0]
	if len(template.TrapCommands) > 1 {
		trapCmd = template.TrapCommands[rand.Intn(len(template.TrapCommands))]
	}

	activeTrap := &ActiveTrap{
		ID:              trapID,
		ToolUseID:       toolUseID,
		TemplateID:      template.ID,
		Category:        template.Category,
		Severity:        template.Severity,
		TrapCommand:     trapCmd,
		OriginalCommand: originalCmd,
		InjectedAt:      time.Now(),
	}

	h.engine.SetActiveTrap(activeTrap)
	h.selector.MarkUsed(template.ID)
	h.cacheTemplate(template)

	// Write trap file for fallback script (must happen before stream injection)
	if err := WriteTrapFile(activeTrap); err != nil {
		h.logger.Error("failed to write trap file", "error", err)
	}

	h.logger.Info("trap registered",
		"trap_id", trapID,
		"tool_use_id", toolUseID,
		"template", template.ID,
		"category", template.Category,
		"trap_command", trapCmd,
	)

	return activeTrap
}

// ResolveTrap handles trap resolution. Reports result, displays training if
// missed, cleans up trap file, and clears active trap. Safe to call multiple
// times - only the first call takes effect.
func (h *CallbackHandler) ResolveTrap(activeTrap *ActiveTrap, result string) {
	// Prevent double-resolution (both hook and request-body detection could fire)
	alreadyResolved := activeTrap.Resolved.Swap(true)
	if alreadyResolved {
		h.logger.Debug("trap already resolved, skipping", "trap_id", activeTrap.ID)
		return
	}

	activeTrap.Triggered.Store(result == "missed")

	h.logger.Info("trap resolved",
		"trap_id", activeTrap.ID,
		"result", result,
		"category", activeTrap.Category,
		"trap_command", activeTrap.TrapCommand,
	)

	go h.reportResult(activeTrap, result)

	if result == "missed" {
		tmpl := h.getCachedTemplate(activeTrap.TemplateID)
		if tmpl != nil {
			DisplayTrainingMessage(os.Stderr, activeTrap, tmpl, "N/A", "N/A")
		}
	}

	if err := RemoveTrapFile(activeTrap.ID); err != nil {
		h.logger.Debug("failed to remove trap file (may not exist)", "error", err)
	}

	h.engine.ClearActiveTrap()
}

func (h *CallbackHandler) reportResult(activeTrap *ActiveTrap, result string) {
	if h.apiClient == nil {
		return
	}

	// Map "expired" to "missed" - the DB constraint only allows missed/caught/edited
	reportResult := result
	if reportResult == "expired" {
		reportResult = "missed"
	}

	responseTimeMs := int(time.Since(activeTrap.InjectedAt).Milliseconds())

	event := &client.TrapEvent{
		TrapTemplateID:  activeTrap.TemplateID,
		TrapCategory:    activeTrap.Category,
		TrapSeverity:    activeTrap.Severity,
		TrapCommand:     activeTrap.TrapCommand,
		OriginalCommand: activeTrap.OriginalCommand,
		Result:          reportResult,
		ResponseTimeMs:  responseTimeMs,
		SessionID:       activeTrap.SessionID,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := h.apiClient.ReportEvent(ctx, event); err != nil {
		h.logger.Error("failed to report trap event", "error", err, "trap_id", activeTrap.ID)
	}
}

func (h *CallbackHandler) cacheTemplate(tmpl *Template) {
	h.cacheMu.Lock()
	defer h.cacheMu.Unlock()
	h.templateCache[tmpl.ID] = tmpl
}

func (h *CallbackHandler) getCachedTemplate(templateID string) *Template {
	h.cacheMu.Lock()
	defer h.cacheMu.Unlock()
	return h.templateCache[templateID]
}
