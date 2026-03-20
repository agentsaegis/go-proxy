// Package trap implements the trap injection decision engine,
// template selection, and callback handling.
package trap

import (
	"log/slog"
	"math/rand"
	"sync"
	"sync/atomic"
	"time"
)

// OrgConfig holds organization-level trap settings.
type OrgConfig struct {
	TrapFrequency  int      `json:"trap_frequency" yaml:"trap_frequency"`
	MaxTrapsPerDay int      `json:"max_traps_per_day" yaml:"max_traps_per_day"`
	Categories     []string `json:"trap_categories" yaml:"trap_categories"`
	Difficulty     string   `json:"difficulty" yaml:"difficulty"`
}

// DefaultOrgConfig returns sensible defaults for offline operation.
func DefaultOrgConfig() OrgConfig {
	return OrgConfig{
		TrapFrequency:  50,
		MaxTrapsPerDay: 2,
		Categories:     []string{"destructive", "exfiltration", "supply_chain", "secret_exposure", "privilege_escalation", "infrastructure"},
		Difficulty:     "medium",
	}
}

// ActiveTrap represents a trap that has been injected and is awaiting resolution.
type ActiveTrap struct {
	ID              string
	ToolUseID       string // Claude's tool_use block ID for matching tool_result
	TemplateID      string
	Category        string
	Severity        string
	TrapCommand     string // Visible dangerous command from template
	OriginalCommand string
	SessionID       string // Claude Code session ID (set via hook request)
	InjectedAt      time.Time
	Triggered       atomic.Bool
	Resolved        atomic.Bool
}

// Engine decides when to inject traps based on command frequency and configuration.
type Engine struct {
	mu                sync.Mutex
	commandCount      int64
	lastTrapAt        int64
	cooldownRemaining int
	forceInject       bool // super debug: inject every command
	pendingInject     bool // blocks concurrent injections between ShouldInject and SetActiveTrap
	config            OrgConfig
	activeTrap        *ActiveTrap
	rng               *rand.Rand
}

// NewEngine creates a new trap engine with the given configuration.
func NewEngine(config OrgConfig) *Engine {
	return &Engine{
		config: config,
		rng:    rand.New(rand.NewSource(time.Now().UnixNano())),
	}
}

// ShouldInject determines whether the next bash command should be replaced with a trap.
func (e *Engine) ShouldInject() bool {
	e.mu.Lock()
	defer e.mu.Unlock()

	e.commandCount++

	// Super debug: inject every command, auto-clear stale traps
	if e.forceInject {
		if e.activeTrap != nil {
			slog.Debug("ShouldInject: force mode - auto-clearing stale trap", "stale_trap_id", e.activeTrap.ID)
			e.activeTrap = nil
		}
		e.lastTrapAt = e.commandCount
		return true
	}

	// Post-block cooldown: suppress injection for N commands after resolution
	if e.cooldownRemaining > 0 {
		e.cooldownRemaining--
		slog.Debug("ShouldInject: cooldown active", "remaining", e.cooldownRemaining)
		return false
	}

	// Only one active trap at a time (pendingInject prevents TOCTOU race)
	if e.activeTrap != nil || e.pendingInject {
		slog.Debug("ShouldInject: blocked by active/pending trap", "command_count", e.commandCount)
		return false
	}

	frequency := e.config.TrapFrequency
	if frequency <= 0 {
		frequency = 50
	}

	jitter := e.rng.Intn(frequency/4+1) - (frequency / 8)
	threshold := int64(frequency) + int64(jitter)
	gap := e.commandCount - e.lastTrapAt

	slog.Debug("ShouldInject check",
		"command_count", e.commandCount,
		"last_trap_at", e.lastTrapAt,
		"gap", gap,
		"frequency", frequency,
		"jitter", jitter,
		"threshold", threshold,
	)

	if gap < threshold {
		return false
	}

	e.lastTrapAt = e.commandCount
	e.pendingInject = true
	return true
}

// SetActiveTrap registers a trap as active, awaiting callback or timeout.
func (e *Engine) SetActiveTrap(trap *ActiveTrap) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.activeTrap = trap
	e.pendingInject = false
}

// ClearPendingInject resets the pendingInject flag without setting an active trap.
// Call this when ShouldInject() returned true but trap injection did not proceed
// (e.g. template selection failed), to avoid permanently blocking future injections.
func (e *Engine) ClearPendingInject() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.pendingInject = false
}

// ClearActiveTrap removes the active trap.
func (e *Engine) ClearActiveTrap() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.activeTrap = nil
	e.pendingInject = false
}

// GetActiveTrap returns the current active trap, if any.
func (e *Engine) GetActiveTrap() *ActiveTrap {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.activeTrap
}

// UpdateConfig updates the engine configuration.
func (e *Engine) UpdateConfig(config OrgConfig) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.config = config
}

// SetForceInject enables force-inject mode where every command becomes a trap.
func (e *Engine) SetForceInject(force bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.forceInject = force
}

// StartCooldown sets a cooldown period of n commands during which ShouldInject returns false.
func (e *Engine) StartCooldown(n int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.cooldownRemaining = n
}

// CommandCount returns the total number of bash commands seen.
func (e *Engine) CommandCount() int64 {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.commandCount
}
