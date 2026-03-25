package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestAddProxyToVSCode_NewFile(t *testing.T) {
	dir := t.TempDir()
	settingsPath := filepath.Join(dir, "Code", "User", "settings.json")

	if err := addProxyToVSCode(settingsPath, "http://localhost:7331"); err != nil {
		t.Fatalf("addProxyToVSCode() error: %v", err)
	}

	data, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("reading settings: %v", err)
	}

	var settings map[string]interface{}
	if err := json.Unmarshal(data, &settings); err != nil {
		t.Fatalf("parsing settings: %v", err)
	}

	if got := settings["http.proxy"]; got != "http://localhost:7331" {
		t.Errorf("http.proxy = %v, want http://localhost:7331", got)
	}
	if got := settings["http.proxyStrictSSL"]; got != false {
		t.Errorf("http.proxyStrictSSL = %v, want false", got)
	}
}

func TestAddProxyToVSCode_MergesExisting(t *testing.T) {
	dir := t.TempDir()
	settingsPath := filepath.Join(dir, "settings.json")

	existing := map[string]interface{}{
		"editor.fontSize": float64(14),
		"theme":           "dark",
	}
	data, _ := json.MarshalIndent(existing, "", "  ")
	if err := os.WriteFile(settingsPath, data, 0o644); err != nil {
		t.Fatal(err)
	}

	if err := addProxyToVSCode(settingsPath, "http://localhost:7331"); err != nil {
		t.Fatalf("addProxyToVSCode() error: %v", err)
	}

	data, _ = os.ReadFile(settingsPath)
	var settings map[string]interface{}
	if err := json.Unmarshal(data, &settings); err != nil {
		t.Fatalf("parsing settings: %v", err)
	}

	// Verify proxy settings added
	if got := settings["http.proxy"]; got != "http://localhost:7331" {
		t.Errorf("http.proxy = %v, want http://localhost:7331", got)
	}

	// Verify existing settings preserved
	if got := settings["editor.fontSize"]; got != float64(14) {
		t.Errorf("editor.fontSize = %v, want 14", got)
	}
	if got := settings["theme"]; got != "dark" {
		t.Errorf("theme = %v, want dark", got)
	}
}

func TestAddProxyToVSCode_JSONC_ReturnsError(t *testing.T) {
	dir := t.TempDir()
	settingsPath := filepath.Join(dir, "settings.json")

	jsonc := []byte(`{
  // this is a comment
  "editor.fontSize": 14
}`)
	if err := os.WriteFile(settingsPath, jsonc, 0o644); err != nil {
		t.Fatal(err)
	}

	err := addProxyToVSCode(settingsPath, "http://localhost:7331")
	if err == nil {
		t.Fatal("expected error for JSONC, got nil")
	}
}

func TestAddProxyToVSCode_OverwritesExistingProxy(t *testing.T) {
	dir := t.TempDir()
	settingsPath := filepath.Join(dir, "settings.json")

	existing := map[string]interface{}{
		"http.proxy": "http://localhost:8080",
	}
	data, _ := json.MarshalIndent(existing, "", "  ")
	if err := os.WriteFile(settingsPath, data, 0o644); err != nil {
		t.Fatal(err)
	}

	if err := addProxyToVSCode(settingsPath, "http://localhost:7331"); err != nil {
		t.Fatalf("addProxyToVSCode() error: %v", err)
	}

	data, _ = os.ReadFile(settingsPath)
	var settings map[string]interface{}
	_ = json.Unmarshal(data, &settings)

	if got := settings["http.proxy"]; got != "http://localhost:7331" {
		t.Errorf("http.proxy = %v, want http://localhost:7331", got)
	}
}

func TestRemoveVSCode_RemovesProxySettings(t *testing.T) {
	home := setupTestHome(t)
	settingsPath, _ := vscodeSettingsPath()

	// Create settings dir and file with our proxy
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0o755); err != nil {
		t.Fatal(err)
	}
	_ = home // used by setupTestHome

	settings := map[string]interface{}{
		"http.proxy":          "http://localhost:7331",
		"http.proxyStrictSSL": false,
		"editor.fontSize":     float64(14),
	}
	data, _ := json.MarshalIndent(settings, "", "  ")
	if err := os.WriteFile(settingsPath, data, 0o644); err != nil {
		t.Fatal(err)
	}

	if err := runRemoveVSCode(nil, nil); err != nil {
		t.Fatalf("runRemoveVSCode() error: %v", err)
	}

	data, _ = os.ReadFile(settingsPath)
	var result map[string]interface{}
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatalf("parsing settings after remove: %v", err)
	}

	if _, exists := result["http.proxy"]; exists {
		t.Error("http.proxy should have been removed")
	}
	if _, exists := result["http.proxyStrictSSL"]; exists {
		t.Error("http.proxyStrictSSL should have been removed")
	}
	if got := result["editor.fontSize"]; got != float64(14) {
		t.Errorf("editor.fontSize = %v, want 14 (should be preserved)", got)
	}
}

func TestRemoveVSCode_SkipsNonLocalProxy(t *testing.T) {
	home := setupTestHome(t)
	settingsPath, _ := vscodeSettingsPath()

	if err := os.MkdirAll(filepath.Dir(settingsPath), 0o755); err != nil {
		t.Fatal(err)
	}
	_ = home

	// Corporate proxy - should NOT be removed
	settings := map[string]interface{}{
		"http.proxy": "http://corp-proxy.example.com:8080",
	}
	data, _ := json.MarshalIndent(settings, "", "  ")
	if err := os.WriteFile(settingsPath, data, 0o644); err != nil {
		t.Fatal(err)
	}

	if err := runRemoveVSCode(nil, nil); err != nil {
		t.Fatalf("runRemoveVSCode() error: %v", err)
	}

	data, _ = os.ReadFile(settingsPath)
	var result map[string]interface{}
	_ = json.Unmarshal(data, &result)

	if got := result["http.proxy"]; got != "http://corp-proxy.example.com:8080" {
		t.Errorf("corporate proxy should not be removed, got %v", got)
	}
}

func TestRemoveVSCode_NoSettingsFile(t *testing.T) {
	_ = setupTestHome(t)
	// No settings file exists - should not error
	if err := runRemoveVSCode(nil, nil); err != nil {
		t.Fatalf("runRemoveVSCode() should not error on missing file: %v", err)
	}
}
