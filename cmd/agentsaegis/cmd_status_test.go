package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRunStatus_NoPIDFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)

	configDir := filepath.Join(dir, ".agentsaegis")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	// Write minimal config so Load() works
	if err := os.WriteFile(filepath.Join(configDir, "config.yaml"), []byte("proxy_port: 7331\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Should not error when no PID file exists (shows STOPPED)
	err := runStatus(nil, nil)
	if err != nil {
		t.Errorf("runStatus() error = %v, want nil", err)
	}
}

func TestRunStatus_StalePIDFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)

	configDir := filepath.Join(dir, ".agentsaegis")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "config.yaml"), []byte("proxy_port: 7331\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Write a stale PID (process that doesn't exist)
	pidFile := filepath.Join(configDir, "aegis.pid")
	if err := os.WriteFile(pidFile, []byte("999999999"), 0o600); err != nil {
		t.Fatal(err)
	}

	err := runStatus(nil, nil)
	if err != nil {
		t.Errorf("runStatus() error = %v, want nil", err)
	}

	// Stale PID file should be cleaned up
	if _, statErr := os.Stat(pidFile); statErr == nil {
		t.Error("stale PID file should have been removed")
	}
}
