package trap

import (
	"sync"
	"testing"
)

func TestDefaultOrgConfig(t *testing.T) {
	cfg := DefaultOrgConfig()

	if cfg.TrapFrequency != 50 {
		t.Errorf("TrapFrequency = %d, want 50", cfg.TrapFrequency)
	}
	if cfg.MaxTrapsPerDay != 2 {
		t.Errorf("MaxTrapsPerDay = %d, want 2", cfg.MaxTrapsPerDay)
	}
	if len(cfg.Categories) == 0 {
		t.Error("Categories is empty")
	}
	if cfg.Difficulty != "medium" {
		t.Errorf("Difficulty = %q, want %q", cfg.Difficulty, "medium")
	}
}

func TestNewEngine(t *testing.T) {
	cfg := DefaultOrgConfig()
	engine := NewEngine(cfg)

	if engine == nil {
		t.Fatal("NewEngine() returned nil")
	}
	if engine.CommandCount() != 0 {
		t.Errorf("CommandCount() = %d, want 0", engine.CommandCount())
	}
	if engine.GetActiveTrap() != nil {
		t.Error("GetActiveTrap() should be nil initially")
	}
}

func TestEngine_CommandCount(t *testing.T) {
	cfg := DefaultOrgConfig()
	cfg.TrapFrequency = 10000 // very high so ShouldInject never fires
	engine := NewEngine(cfg)

	for i := 0; i < 10; i++ {
		engine.ShouldInject()
	}

	if engine.CommandCount() != 10 {
		t.Errorf("CommandCount() = %d, want 10", engine.CommandCount())
	}
}

func TestEngine_ShouldInject_RespectsActiveTrap(t *testing.T) {
	cfg := DefaultOrgConfig()
	cfg.TrapFrequency = 1 // inject every time
	engine := NewEngine(cfg)

	// Force enough commands to pass threshold
	for i := 0; i < 100; i++ {
		engine.ShouldInject()
	}

	// Set an active trap - should prevent further injections
	engine.SetActiveTrap(&ActiveTrap{ID: "test_trap"})

	for i := 0; i < 10; i++ {
		if engine.ShouldInject() {
			t.Error("ShouldInject() = true while active trap is set")
		}
	}

	// Clear the active trap - should allow injection again
	engine.ClearActiveTrap()
}

func TestEngine_ShouldInject_NeverTooEarly(t *testing.T) {
	cfg := DefaultOrgConfig()
	cfg.TrapFrequency = 100
	engine := NewEngine(cfg)

	// With frequency=100, first injection should not happen at command 1
	if engine.ShouldInject() {
		t.Error("ShouldInject() = true on first command with frequency 100")
	}
}

func TestEngine_ShouldInject_EventuallyTrue(t *testing.T) {
	cfg := DefaultOrgConfig()
	cfg.TrapFrequency = 5
	engine := NewEngine(cfg)

	injected := false
	for i := 0; i < 1000; i++ {
		if engine.ShouldInject() {
			injected = true
			break
		}
	}

	if !injected {
		t.Error("ShouldInject() never returned true after 1000 commands with frequency 5")
	}
}

func TestEngine_ShouldInject_ZeroFrequency(t *testing.T) {
	cfg := DefaultOrgConfig()
	cfg.TrapFrequency = 0 // should default to 50
	engine := NewEngine(cfg)

	// Just verify it does not panic and eventually returns
	for i := 0; i < 200; i++ {
		engine.ShouldInject()
	}
}

func TestEngine_SetGetClearActiveTrap(t *testing.T) {
	engine := NewEngine(DefaultOrgConfig())

	if engine.GetActiveTrap() != nil {
		t.Error("GetActiveTrap() should be nil initially")
	}

	trap := &ActiveTrap{ID: "trap_abc", Category: "destructive"}
	engine.SetActiveTrap(trap)

	got := engine.GetActiveTrap()
	if got == nil {
		t.Fatal("GetActiveTrap() = nil after SetActiveTrap")
	}
	if got.ID != "trap_abc" {
		t.Errorf("ActiveTrap.ID = %q, want %q", got.ID, "trap_abc")
	}

	engine.ClearActiveTrap()
	if engine.GetActiveTrap() != nil {
		t.Error("GetActiveTrap() should be nil after ClearActiveTrap")
	}
}

func TestEngine_UpdateConfig(t *testing.T) {
	engine := NewEngine(DefaultOrgConfig())

	newCfg := OrgConfig{
		TrapFrequency:  10,
		MaxTrapsPerDay: 5,
		Categories:     []string{"destructive"},
		Difficulty:     "hard",
	}

	engine.UpdateConfig(newCfg)

	// Verify the config is used (indirectly through behavior)
	// With frequency 10, should inject sooner than with frequency 50
	injected := false
	for i := 0; i < 100; i++ {
		if engine.ShouldInject() {
			injected = true
			break
		}
	}
	if !injected {
		t.Error("expected injection with frequency=10 within 100 commands")
	}
}

func TestEngine_Concurrency(t *testing.T) {
	engine := NewEngine(DefaultOrgConfig())
	var wg sync.WaitGroup

	// Run ShouldInject concurrently from many goroutines
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				engine.ShouldInject()
			}
		}()
	}

	// Concurrently set/get/clear active trap
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			trap := &ActiveTrap{ID: "concurrent_trap"}
			engine.SetActiveTrap(trap)
			engine.GetActiveTrap()
			engine.ClearActiveTrap()
			engine.CommandCount()
		}(i)
	}

	wg.Wait()

	// Total commands should be 50 * 20 = 1000
	if engine.CommandCount() != 1000 {
		t.Errorf("CommandCount() = %d, want 1000", engine.CommandCount())
	}
}

func TestEngine_SetForceInject(t *testing.T) {
	engine := NewEngine(DefaultOrgConfig())

	// With force inject, every call to ShouldInject should return true
	engine.SetForceInject(true)

	for i := 0; i < 5; i++ {
		if !engine.ShouldInject() {
			t.Errorf("ShouldInject() = false on command %d with force inject", i)
		}
	}

	engine.SetForceInject(false)
	// After disabling, should behave normally (not necessarily true on first call)
}

func TestEngine_StartCooldown(t *testing.T) {
	engine := NewEngine(OrgConfig{TrapFrequency: 1, MaxTrapsPerDay: 100})

	// Force injection
	engine.SetForceInject(true)
	if !engine.ShouldInject() {
		t.Fatal("expected injection in force mode")
	}

	// Start cooldown
	engine.SetForceInject(false)
	engine.StartCooldown(5)

	// During cooldown, should not inject
	for i := 0; i < 5; i++ {
		if engine.ShouldInject() {
			t.Errorf("ShouldInject() = true during cooldown at command %d", i)
		}
	}
}

func TestEngine_ForceInject_ClearsStaleTraps(t *testing.T) {
	engine := NewEngine(DefaultOrgConfig())
	engine.SetForceInject(true)

	// Set a stale trap
	engine.SetActiveTrap(&ActiveTrap{ID: "stale"})

	// Force inject should auto-clear the stale trap
	if !engine.ShouldInject() {
		t.Error("ShouldInject() = false in force mode with stale trap")
	}

	// The stale trap should have been cleared
	if engine.GetActiveTrap() != nil {
		t.Error("stale trap should have been cleared")
	}
}

func TestActiveTrap_Triggered(t *testing.T) {
	at := &ActiveTrap{ID: "test"}

	if at.Triggered.Load() {
		t.Error("Triggered should be false initially")
	}

	at.Triggered.Store(true)
	if !at.Triggered.Load() {
		t.Error("Triggered should be true after Store(true)")
	}
}
