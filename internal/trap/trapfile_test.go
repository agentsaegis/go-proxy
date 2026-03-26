package trap

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func setupTestTrapDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	trapDir := filepath.Join(dir, ".agentsaegis", "traps")
	if err := os.MkdirAll(trapDir, 0o700); err != nil {
		t.Fatalf("creating test trap dir: %v", err)
	}
	return dir
}

func TestWriteAndReadTrapFile(t *testing.T) {
	setupTestTrapDir(t)

	trap := &ActiveTrap{
		ID:          "trap_test_123",
		TrapCommand: "rm -rf .git .env",
		TemplateID:  "trap_rm_rf_expand",
		Category:    "destructive",
		Severity:    "critical",
		InjectedAt:  time.Now(),
	}

	if err := WriteTrapFile(trap); err != nil {
		t.Fatalf("WriteTrapFile: %v", err)
	}

	entry, err := ReadTrapFile("trap_test_123")
	if err != nil {
		t.Fatalf("ReadTrapFile: %v", err)
	}

	if entry.ID != "trap_test_123" {
		t.Errorf("ID = %q, want %q", entry.ID, "trap_test_123")
	}
	if entry.TrapCommand != "rm -rf .git .env" {
		t.Errorf("TrapCommand = %q, want %q", entry.TrapCommand, "rm -rf .git .env")
	}
	if entry.ExpiresAt.Before(entry.InjectedAt) {
		t.Error("ExpiresAt should be after InjectedAt")
	}
}

func TestRemoveTrapFile(t *testing.T) {
	setupTestTrapDir(t)

	trap := &ActiveTrap{
		ID:         "trap_remove_test",
		InjectedAt: time.Now(),
	}

	if err := WriteTrapFile(trap); err != nil {
		t.Fatalf("WriteTrapFile: %v", err)
	}

	if err := RemoveTrapFile("trap_remove_test"); err != nil {
		t.Fatalf("RemoveTrapFile: %v", err)
	}

	_, err := ReadTrapFile("trap_remove_test")
	if err == nil {
		t.Error("ReadTrapFile should fail after RemoveTrapFile")
	}
}

func TestCleanStaleTrapFiles(t *testing.T) {
	home := setupTestTrapDir(t)
	trapDir := filepath.Join(home, ".agentsaegis", "traps")

	// Create a "stale" file with old modification time
	stalePath := filepath.Join(trapDir, "trap_stale.json")
	if err := os.WriteFile(stalePath, []byte(`{"id":"trap_stale"}`), 0o600); err != nil {
		t.Fatalf("creating stale file: %v", err)
	}
	staleTime := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(stalePath, staleTime, staleTime); err != nil {
		t.Fatalf("setting stale time: %v", err)
	}

	// Create a fresh file
	freshPath := filepath.Join(trapDir, "trap_fresh.json")
	if err := os.WriteFile(freshPath, []byte(`{"id":"trap_fresh"}`), 0o600); err != nil {
		t.Fatalf("creating fresh file: %v", err)
	}

	if err := CleanStaleTrapFiles(1 * time.Hour); err != nil {
		t.Fatalf("CleanStaleTrapFiles: %v", err)
	}

	// Stale file should be removed
	if _, err := os.Stat(stalePath); !os.IsNotExist(err) {
		t.Error("stale file should have been removed")
	}

	// Fresh file should remain
	if _, err := os.Stat(freshPath); err != nil {
		t.Error("fresh file should still exist")
	}
}

func TestCleanStaleTrapFiles_RemoveAll(t *testing.T) {
	home := setupTestTrapDir(t)
	trapDir := filepath.Join(home, ".agentsaegis", "traps")

	for _, name := range []string{"trap_1.json", "trap_2.json"} {
		if err := os.WriteFile(filepath.Join(trapDir, name), []byte(`{}`), 0o600); err != nil {
			t.Fatalf("creating file: %v", err)
		}
	}

	if err := CleanStaleTrapFiles(0); err != nil {
		t.Fatalf("CleanStaleTrapFiles(0): %v", err)
	}

	entries, _ := os.ReadDir(trapDir)
	if len(entries) != 0 {
		t.Errorf("expected empty dir, got %d files", len(entries))
	}
}

func TestHasActiveTrapFiles(t *testing.T) {
	setupTestTrapDir(t)

	if HasActiveTrapFiles() {
		t.Error("should be false with empty dir")
	}

	trap := &ActiveTrap{ID: "trap_active", InjectedAt: time.Now()}
	if err := WriteTrapFile(trap); err != nil {
		t.Fatalf("WriteTrapFile: %v", err)
	}

	if !HasActiveTrapFiles() {
		t.Error("should be true after writing trap file")
	}
}

func TestReadAllActiveTrapFiles(t *testing.T) {
	setupTestTrapDir(t)

	// Write two traps
	trap1 := &ActiveTrap{
		ID:          "trap_active_1",
		TrapCommand: "rm -rf /tmp/.aegis-trap-test",
		TemplateID:  "rm-rf",
		Category:    "destructive",
		Severity:    "critical",
		InjectedAt:  time.Now(),
	}
	trap2 := &ActiveTrap{
		ID:          "trap_active_2",
		TrapCommand: "curl http://0.0.0.0:9999/exfil",
		TemplateID:  "exfil",
		Category:    "exfiltration",
		Severity:    "high",
		InjectedAt:  time.Now(),
	}

	if err := WriteTrapFile(trap1); err != nil {
		t.Fatalf("WriteTrapFile: %v", err)
	}
	if err := WriteTrapFile(trap2); err != nil {
		t.Fatalf("WriteTrapFile: %v", err)
	}

	files, err := ReadAllActiveTrapFiles()
	if err != nil {
		t.Fatalf("ReadAllActiveTrapFiles: %v", err)
	}

	if len(files) != 2 {
		t.Fatalf("got %d files, want 2", len(files))
	}

	// Verify fields are populated
	found := false
	for _, f := range files {
		if f.ID == "trap_active_1" {
			found = true
			if f.TrapCommand != "rm -rf /tmp/.aegis-trap-test" {
				t.Errorf("TrapCommand = %q, want %q", f.TrapCommand, "rm -rf /tmp/.aegis-trap-test")
			}
			if f.Category != "destructive" {
				t.Errorf("Category = %q, want %q", f.Category, "destructive")
			}
		}
	}
	if !found {
		t.Error("trap_active_1 not found in results")
	}
}

func TestReadAllActiveTrapFiles_Empty(t *testing.T) {
	setupTestTrapDir(t)

	files, err := ReadAllActiveTrapFiles()
	if err != nil {
		t.Fatalf("ReadAllActiveTrapFiles: %v", err)
	}
	if len(files) != 0 {
		t.Errorf("got %d files, want 0", len(files))
	}
}

func TestReadAllActiveTrapFiles_NoDir(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)

	files, err := ReadAllActiveTrapFiles()
	if err != nil {
		t.Fatalf("ReadAllActiveTrapFiles: %v", err)
	}
	if files != nil {
		t.Errorf("expected nil, got %v", files)
	}
}

func TestCleanStaleTrapFiles_NoDir(t *testing.T) {
	// Point HOME to a temp dir without .agentsaegis/traps
	dir := t.TempDir()
	t.Setenv("HOME", dir)

	// Should not error - just return nil
	if err := CleanStaleTrapFiles(time.Hour); err != nil {
		t.Fatalf("CleanStaleTrapFiles on missing dir: %v", err)
	}
}
