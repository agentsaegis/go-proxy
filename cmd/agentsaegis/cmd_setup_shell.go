package main

import (
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
func shellWrapper(port int) string {
	return fmt.Sprintf(`%s
# Routes Claude Code through the security proxy when the proxy is running.
# If the proxy is down, Claude Code talks directly to the Anthropic API.
claude() {
  if curl -sf --max-time 1 http://localhost:%d/__aegis/health > /dev/null 2>&1; then
    ANTHROPIC_BASE_URL=http://localhost:%d command claude "$@"
  else
    command claude "$@"
  fi
}
%s`, markerBegin, port, port, markerEnd)
}

// fishWrapper returns the wrapper function for fish shell.
func fishWrapper(port int) string {
	return fmt.Sprintf(`%s
# Routes Claude Code through the security proxy when the proxy is running.
# If the proxy is down, Claude Code talks directly to the Anthropic API.
function claude
  if curl -sf --max-time 1 http://localhost:%d/__aegis/health > /dev/null 2>&1
    set -lx ANTHROPIC_BASE_URL http://localhost:%d
    command claude $argv
  else
    command claude $argv
  end
end
%s`, markerBegin, port, port, markerEnd)
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
		wrapper = fishWrapper(cfg.ProxyPort)
	} else {
		wrapper = shellWrapper(cfg.ProxyPort)
	}

	for _, profilePath := range paths {
		if err := installWrapper(profilePath, wrapper); err != nil {
			return err
		}
		relPath := profilePath
		if strings.HasPrefix(profilePath, homeDir) {
			relPath = "~" + profilePath[len(homeDir):]
		}
		fmt.Printf("Added Claude Code wrapper to %s. Run: source %s\n", relPath, relPath)
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

	return nil
}
