package main

import (
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"time"

	"github.com/spf13/cobra"

	"github.com/agentsaegis/go-proxy/internal/config"
)

var launchCmd = &cobra.Command{
	Use:   "launch claude-desktop",
	Short: "Launch Claude Desktop with proxy environment",
	Long:  "Starts the proxy if needed, then launches Claude Desktop with --proxy-server pointing to the proxy for TLS MITM interception. Requires the proxy CA to be trusted (run: sudo agentsaegis trust-cert).",
	RunE:  runLaunch,
}

func init() {
	rootCmd.AddCommand(launchCmd)
}

func runLaunch(_ *cobra.Command, args []string) error {
	cfg, err := config.Load()
	if err != nil {
		cfg = config.DefaultConfig()
	}

	proxyURL := fmt.Sprintf("http://localhost:%d", cfg.ProxyPort)
	healthURL := fmt.Sprintf("%s/__aegis/health", proxyURL)

	// Check if proxy is running
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(healthURL)
	if err != nil || resp.StatusCode != http.StatusOK {
		// Start proxy as daemon
		fmt.Println("Starting proxy daemon...")
		exe, exeErr := os.Executable()
		if exeErr != nil {
			return fmt.Errorf("finding executable: %w", exeErr)
		}
		startCmd := exec.Command(exe, "start", "--daemon")
		startCmd.Stdout = os.Stdout
		startCmd.Stderr = os.Stderr
		if startErr := startCmd.Run(); startErr != nil {
			return fmt.Errorf("starting proxy: %w", startErr)
		}
		// Wait for health
		for i := 0; i < 6; i++ {
			time.Sleep(500 * time.Millisecond)
			if r, e := client.Get(healthURL); e == nil && r.StatusCode == http.StatusOK {
				break
			}
		}
	}

	// Launch Claude Desktop
	appPath := "/Applications/Claude.app/Contents/MacOS/Claude"
	if _, err := os.Stat(appPath); err != nil {
		return fmt.Errorf("claude Desktop not found at %s", appPath)
	}

	configDir, _ := config.ConfigDir()
	caPath := configDir + "/ca.pem"

	// Warn if CA cert is missing - trust-cert must be run first
	if _, err := os.Stat(caPath); err != nil {
		fmt.Println("Warning: proxy CA not found. Start the proxy first, then run: sudo agentsaegis trust-cert")
	} else if !isCATrusted(caPath) {
		fmt.Println("Warning: proxy CA is not trusted. Run: sudo agentsaegis trust-cert")
	}

	// Use --proxy-server so Chromium routes traffic through the proxy.
	// HTTPS_PROXY is ignored by Electron's Chromium networking stack.
	cmd := exec.Command(appPath, // nosemgrep: dangerous-exec-command -- appPath is a hardcoded constant
		fmt.Sprintf("--proxy-server=%s", proxyURL),
	)
	cmd.Env = append(os.Environ(),
		fmt.Sprintf("NODE_EXTRA_CA_CERTS=%s", caPath),
	)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("launching Claude Desktop: %w", err)
	}

	fmt.Printf("Claude Desktop launched with --proxy-server=%s NODE_EXTRA_CA_CERTS=%s\n", proxyURL, caPath)
	return nil
}

// isCATrusted checks whether the given CA cert is trusted by the system on macOS.
// Returns true on non-macOS platforms (no check available) or if the check passes.
func isCATrusted(caPath string) bool {
	out := exec.Command("security", "verify-cert", "-c", caPath) // nosemgrep: dangerous-exec-command -- caPath comes from config dir
	return out.Run() == nil
}
