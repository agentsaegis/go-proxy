package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/spf13/cobra"

	"github.com/agentsaegis/go-proxy/internal/config"
)

const (
	markerBegin = "# >>> agentsaegis >>>"
	markerEnd   = "# <<< agentsaegis <<<"
)

var setupShellCmd = &cobra.Command{
	Use:   "setup-shell",
	Short: "Configure shell to route Claude Code through the proxy when running",
	RunE:  runSetupShell,
}

var removeShellCmd = &cobra.Command{
	Use:   "remove-shell",
	Short: "Remove AgentsAegis shell configuration",
	RunE:  runRemoveShell,
}

func init() {
	rootCmd.AddCommand(setupShellCmd)
	rootCmd.AddCommand(removeShellCmd)
}

// shellWrapper returns the wrapper function for bash/zsh shells.
func shellWrapper(port int, exe string) string {
	return fmt.Sprintf(`%s
# Ensures the AgentsAegis proxy is running, starting it if needed.
_aegis_ensure_proxy() {
  local port=$1 _exe=$2
  if curl -sf --max-time 1 http://localhost:${port}/__aegis/health > /dev/null 2>&1; then
    return 0
  fi
  "$_exe" start --daemon > /dev/null 2>&1
  local i=0
  while [ $i -lt 6 ]; do
    sleep 0.5
    if curl -sf --max-time 1 http://localhost:${port}/__aegis/health > /dev/null 2>&1; then
      return 0
    fi
    i=$((i + 1))
  done
  return 1
}
# Routes Claude Code through the security proxy. Auto-starts the proxy if needed.
claude() {
  local _p=%d _e="%s"
  if _aegis_ensure_proxy "$_p" "$_e"; then
    ANTHROPIC_BASE_URL=http://localhost:${_p} command claude "$@"
  else
    command claude "$@"
  fi
  local _x=$?
  if [ $_x -ne 0 ]; then
    if ! curl -sf --max-time 1 http://localhost:${_p}/__aegis/health > /dev/null 2>&1; then
      "$_e" start --daemon > /dev/null 2>&1
    fi
  fi
  return $_x
}
# Routes Copilot CLI through the HTTPS proxy for AI API interception. Auto-starts the proxy if needed.
copilot() {
  local _p=%d _e="%s"
  if _aegis_ensure_proxy "$_p" "$_e"; then
    HTTPS_PROXY=http://localhost:${_p} command copilot "$@"
  else
    command copilot "$@"
  fi
  local _x=$?
  if [ $_x -ne 0 ]; then
    if ! curl -sf --max-time 1 http://localhost:${_p}/__aegis/health > /dev/null 2>&1; then
      "$_e" start --daemon > /dev/null 2>&1
    fi
  fi
  return $_x
}
%s`, markerBegin, port, exe, port, exe, markerEnd)
}

// fishWrapper returns the wrapper function for fish shell.
func fishWrapper(port int, exe string) string {
	return fmt.Sprintf(`%s
# Ensures the AgentsAegis proxy is running, starting it if needed.
function _aegis_ensure_proxy
  set -l port $argv[1]
  set -l _exe $argv[2]
  if curl -sf --max-time 1 http://localhost:$port/__aegis/health > /dev/null 2>&1
    return 0
  end
  "$_exe" start --daemon > /dev/null 2>&1
  for i in (seq 6)
    sleep 0.5
    if curl -sf --max-time 1 http://localhost:$port/__aegis/health > /dev/null 2>&1
      return 0
    end
  end
  return 1
end
# Routes Claude Code through the security proxy. Auto-starts the proxy if needed.
function claude
  set -l _p %d
  set -l _e "%s"
  if _aegis_ensure_proxy $_p $_e
    set -lx ANTHROPIC_BASE_URL http://localhost:$_p
    command claude $argv
  else
    command claude $argv
  end
  set -l _x $status
  if test $_x -ne 0
    if not curl -sf --max-time 1 http://localhost:$_p/__aegis/health > /dev/null 2>&1
      "$_e" start --daemon > /dev/null 2>&1
    end
  end
  return $_x
end
# Routes Copilot CLI through the HTTPS proxy for AI API interception. Auto-starts the proxy if needed.
function copilot
  set -l _p %d
  set -l _e "%s"
  if _aegis_ensure_proxy $_p $_e
    set -lx HTTPS_PROXY http://localhost:$_p
    command copilot $argv
  else
    command copilot $argv
  end
  set -l _x $status
  if test $_x -ne 0
    if not curl -sf --max-time 1 http://localhost:$_p/__aegis/health > /dev/null 2>&1
      "$_e" start --daemon > /dev/null 2>&1
    end
  end
  return $_x
end
%s`, markerBegin, port, exe, port, exe, markerEnd)
}

// removeMarkerBlock removes everything between the agentsaegis markers (inclusive).
func removeMarkerBlock(content string) string {
	re := regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(markerBegin) + `\n[\s\S]*?` + regexp.QuoteMeta(markerEnd) + `\n?`)
	return re.ReplaceAllString(content, "")
}

// removeLegacyLines removes old-style AgentsAegis lines (bare export + comment).
func removeLegacyLines(content string) string {
	lines := strings.Split(content, "\n")
	var out []string
	for i := 0; i < len(lines); i++ {
		trimmed := strings.TrimSpace(lines[i])
		// Skip old comment line
		if trimmed == "# AgentsAegis proxy - route Claude Code through security proxy" {
			// Also skip the next line if it's the export
			if i+1 < len(lines) && strings.Contains(lines[i+1], "ANTHROPIC_BASE_URL") {
				i++
			}
			continue
		}
		// Skip standalone old export line
		if strings.HasPrefix(trimmed, "export ANTHROPIC_BASE_URL=http://localhost:") && strings.Contains(trimmed, "ANTHROPIC_BASE_URL") {
			continue
		}
		if strings.HasPrefix(trimmed, "set -gx ANTHROPIC_BASE_URL http://localhost:") {
			continue
		}
		out = append(out, lines[i])
	}
	return strings.Join(out, "\n")
}

// shellProfiles returns the list of profile paths to update for the current shell.
func shellProfiles(homeDir string) (paths []string, isFish bool) {
	shell := os.Getenv("SHELL")

	switch {
	case strings.Contains(shell, "zsh"):
		zshrc := filepath.Join(homeDir, ".zshrc")
		if _, err := os.Stat(zshrc); err == nil {
			paths = append(paths, zshrc)
		} else {
			// Create it
			paths = append(paths, zshrc)
		}
	case strings.Contains(shell, "bash"):
		bashrc := filepath.Join(homeDir, ".bashrc")
		if _, err := os.Stat(bashrc); err == nil {
			paths = append(paths, bashrc)
		} else {
			paths = append(paths, bashrc)
		}
	case strings.Contains(shell, "fish"):
		fishConfig := filepath.Join(homeDir, ".config", "fish", "config.fish")
		paths = append(paths, fishConfig)
		isFish = true
	}

	return paths, isFish
}

func runSetupShell(_ *cobra.Command, _ []string) error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}

	homeDir, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("getting home directory: %w", err)
	}

	// Resolve binary path for the copilot hook command
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("finding executable path: %w", err)
	}
	exe, err = filepath.EvalSymlinks(exe)
	if err != nil {
		return fmt.Errorf("resolving executable path: %w", err)
	}

	paths, isFish := shellProfiles(homeDir)
	if len(paths) == 0 {
		shell := os.Getenv("SHELL")
		fmt.Printf("Unrecognized shell: %s\n", shell)
		fmt.Println("Add a claude() wrapper function to your shell profile manually.")
		fmt.Printf("See: agentsaegis setup-shell --help\n")
		return nil
	}

	var wrapper string
	if isFish {
		wrapper = fishWrapper(cfg.ProxyPort, exe)
	} else {
		wrapper = shellWrapper(cfg.ProxyPort, exe)
	}

	for _, profilePath := range paths {
		if err := installWrapper(profilePath, wrapper); err != nil {
			return err
		}
		relPath := profilePath
		if strings.HasPrefix(profilePath, homeDir) {
			relPath = "~" + profilePath[len(homeDir):]
		}
		fmt.Printf("Added Claude Code + Copilot wrappers to %s. Run: source %s\n", relPath, relPath)
	}

	// Configure Claude Code PreToolUse hook in ~/.claude/settings.json
	if err := installClaudeHook(homeDir, exe); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: could not configure Claude Code hook: %v\n", err)
		fmt.Fprintln(os.Stderr, "You can manually add the PreToolUse hook to ~/.claude/settings.json")
	} else {
		fmt.Println("Configured PreToolUse hook in ~/.claude/settings.json")
	}

	return nil
}

func installWrapper(profilePath, wrapper string) error {
	content := ""
	existing, err := os.ReadFile(profilePath)
	if err == nil {
		content = string(existing)
	}

	// Remove any existing marker block
	content = removeMarkerBlock(content)

	// Remove legacy lines (old-style export)
	content = removeLegacyLines(content)

	// Trim trailing whitespace and add the wrapper
	content = strings.TrimRight(content, "\n\t ") + "\n\n" + wrapper + "\n"

	if err := os.MkdirAll(filepath.Dir(profilePath), 0o755); err != nil {
		return fmt.Errorf("creating directory for %s: %w", profilePath, err)
	}

	if err := os.WriteFile(profilePath, []byte(content), 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", profilePath, err)
	}

	return nil
}

func runRemoveShell(_ *cobra.Command, _ []string) error {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("getting home directory: %w", err)
	}

	paths, _ := shellProfiles(homeDir)
	if len(paths) == 0 {
		fmt.Println("No shell profiles found to clean up.")
		return nil
	}

	removed := false
	for _, profilePath := range paths {
		existing, readErr := os.ReadFile(profilePath)
		if readErr != nil {
			continue
		}

		content := string(existing)
		cleaned := removeMarkerBlock(content)
		cleaned = removeLegacyLines(cleaned)

		if cleaned != content {
			cleaned = strings.TrimRight(cleaned, "\n\t ") + "\n"
			if writeErr := os.WriteFile(profilePath, []byte(cleaned), 0o644); writeErr != nil {
				return fmt.Errorf("writing %s: %w", profilePath, writeErr)
			}
			relPath := profilePath
			if strings.HasPrefix(profilePath, homeDir) {
				relPath = "~" + profilePath[len(homeDir):]
			}
			fmt.Printf("Removed AgentsAegis configuration from %s\n", relPath)
			removed = true
		}
	}

	if !removed {
		fmt.Println("No AgentsAegis configuration found in shell profiles.")
	}

	// Remove Claude Code hook from settings.json
	if homeDir, err := os.UserHomeDir(); err == nil {
		if removeErr := removeClaudeHook(homeDir); removeErr == nil {
			fmt.Println("Removed PreToolUse hook from ~/.claude/settings.json")
		}
	}

	return nil
}

// installClaudeHook adds an AgentsAegis PreToolUse hook to ~/.claude/settings.json.
// It merges with existing settings, preserving other hooks and configuration.
func installClaudeHook(homeDir, exePath string) error {
	settingsPath := filepath.Join(homeDir, ".claude", "settings.json")

	// Read existing settings (or start fresh)
	var settings map[string]interface{}
	data, err := os.ReadFile(settingsPath)
	if err == nil {
		if err := json.Unmarshal(data, &settings); err != nil {
			settings = make(map[string]interface{})
		}
	} else {
		settings = make(map[string]interface{})
	}

	// Get or create hooks map
	hooks, _ := settings["hooks"].(map[string]interface{})
	if hooks == nil {
		hooks = make(map[string]interface{})
	}

	hookCommand := exePath + " hook"

	// Build the AgentsAegis PreToolUse hook entry
	aegisHook := map[string]interface{}{
		"matcher": "Bash",
		"hooks": []interface{}{
			map[string]interface{}{
				"type":    "command",
				"command": hookCommand,
			},
		},
	}

	// Get existing PreToolUse hooks and replace/add the AgentsAegis one
	var preToolUseHooks []interface{}
	if existing, ok := hooks["PreToolUse"].([]interface{}); ok {
		// Remove any existing AgentsAegis hook (by matching command containing "agentsaegis hook")
		for _, h := range existing {
			hookMap, ok := h.(map[string]interface{})
			if !ok {
				preToolUseHooks = append(preToolUseHooks, h)
				continue
			}
			hooksList, _ := hookMap["hooks"].([]interface{})
			isAegis := false
			for _, inner := range hooksList {
				innerMap, ok := inner.(map[string]interface{})
				if !ok {
					continue
				}
				cmd, _ := innerMap["command"].(string)
				if strings.Contains(cmd, "agentsaegis hook") {
					isAegis = true
					break
				}
			}
			if !isAegis {
				preToolUseHooks = append(preToolUseHooks, h)
			}
		}
	}
	preToolUseHooks = append(preToolUseHooks, aegisHook)
	hooks["PreToolUse"] = preToolUseHooks
	settings["hooks"] = hooks

	// Write back
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0o700); err != nil {
		return fmt.Errorf("creating ~/.claude directory: %w", err)
	}

	out, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling settings: %w", err)
	}

	return os.WriteFile(settingsPath, out, 0o600)
}

// removeClaudeHook removes the AgentsAegis PreToolUse hook from ~/.claude/settings.json.
func removeClaudeHook(homeDir string) error {
	settingsPath := filepath.Join(homeDir, ".claude", "settings.json")

	data, err := os.ReadFile(settingsPath)
	if err != nil {
		return nil // No settings file - nothing to remove
	}

	var settings map[string]interface{}
	if err := json.Unmarshal(data, &settings); err != nil {
		return nil
	}

	hooks, _ := settings["hooks"].(map[string]interface{})
	if hooks == nil {
		return nil
	}

	existing, ok := hooks["PreToolUse"].([]interface{})
	if !ok || len(existing) == 0 {
		return nil
	}

	// Filter out AgentsAegis hooks
	var filtered []interface{}
	for _, h := range existing {
		hookMap, ok := h.(map[string]interface{})
		if !ok {
			filtered = append(filtered, h)
			continue
		}
		hooksList, _ := hookMap["hooks"].([]interface{})
		isAegis := false
		for _, inner := range hooksList {
			innerMap, ok := inner.(map[string]interface{})
			if !ok {
				continue
			}
			cmd, _ := innerMap["command"].(string)
			if strings.Contains(cmd, "agentsaegis hook") {
				isAegis = true
				break
			}
		}
		if !isAegis {
			filtered = append(filtered, h)
		}
	}

	if len(filtered) == 0 {
		delete(hooks, "PreToolUse")
	} else {
		hooks["PreToolUse"] = filtered
	}

	if len(hooks) == 0 {
		delete(settings, "hooks")
	}

	out, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(settingsPath, out, 0o600)
}
