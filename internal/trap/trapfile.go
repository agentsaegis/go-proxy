package trap

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// trapFileEntry is the JSON structure written to disk for the fallback script.
type trapFileEntry struct {
	ID          string    `json:"id"`
	TrapCommand string    `json:"trap_command"`
	TemplateID  string    `json:"template_id"`
	Category    string    `json:"category"`
	Severity    string    `json:"severity"`
	InjectedAt  time.Time `json:"injected_at"`
	ExpiresAt   time.Time `json:"expires_at"`
}

const trapFileTTL = 2 * time.Minute

// TrapFileDir returns the directory where trap files are stored.
func TrapFileDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("getting home dir: %w", err)
	}
	return filepath.Join(home, ".agentsaegis", "traps"), nil
}

// WriteTrapFile atomically writes a trap file for the fallback script.
// Uses write-to-temp + rename for atomicity.
func WriteTrapFile(trap *ActiveTrap) error {
	dir, err := TrapFileDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("creating trap dir: %w", err)
	}

	now := trap.InjectedAt
	if now.IsZero() {
		now = time.Now()
	}

	entry := trapFileEntry{
		ID:          trap.ID,
		TrapCommand: trap.TrapCommand,
		TemplateID:  trap.TemplateID,
		Category:    trap.Category,
		Severity:    trap.Severity,
		InjectedAt:  now,
		ExpiresAt:   now.Add(trapFileTTL),
	}

	data, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("marshaling trap file: %w", err)
	}

	finalPath := filepath.Join(dir, trap.ID+".json")
	tmpPath := finalPath + ".tmp"

	if err := os.WriteFile(tmpPath, data, 0o600); err != nil {
		return fmt.Errorf("writing temp trap file: %w", err)
	}

	if err := os.Rename(tmpPath, finalPath); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("renaming trap file: %w", err)
	}

	return nil
}

// ReadTrapFile reads a single trap file by ID.
func ReadTrapFile(trapID string) (*trapFileEntry, error) {
	dir, err := TrapFileDir()
	if err != nil {
		return nil, err
	}

	data, err := os.ReadFile(filepath.Join(dir, trapID+".json"))
	if err != nil {
		return nil, fmt.Errorf("reading trap file: %w", err)
	}

	var entry trapFileEntry
	if err := json.Unmarshal(data, &entry); err != nil {
		return nil, fmt.Errorf("parsing trap file: %w", err)
	}

	return &entry, nil
}

// RemoveTrapFile deletes a trap file after resolution.
func RemoveTrapFile(trapID string) error {
	dir, err := TrapFileDir()
	if err != nil {
		return err
	}
	return os.Remove(filepath.Join(dir, trapID+".json"))
}

// CleanStaleTrapFiles removes trap files older than maxAge.
// Pass 0 to remove all trap files.
func CleanStaleTrapFiles(maxAge time.Duration) error {
	dir, err := TrapFileDir()
	if err != nil {
		return err
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("reading trap dir: %w", err)
	}

	now := time.Now()
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}

		if maxAge == 0 {
			_ = os.Remove(filepath.Join(dir, e.Name()))
			continue
		}

		info, err := e.Info()
		if err != nil {
			continue
		}
		if now.Sub(info.ModTime()) > maxAge {
			_ = os.Remove(filepath.Join(dir, e.Name()))
		}
	}

	return nil
}

// HasActiveTrapFiles returns true if any trap files exist in the directory.
func HasActiveTrapFiles() bool {
	dir, err := TrapFileDir()
	if err != nil {
		return false
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}

	for _, e := range entries {
		if !e.IsDir() && filepath.Ext(e.Name()) == ".json" {
			return true
		}
	}
	return false
}
