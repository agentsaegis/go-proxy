package main

import (
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"runtime"
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
	if err == nil {
		resp.Body.Close()
	}
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
			if r, e := client.Get(healthURL); e == nil {
				r.Body.Close()
				if r.StatusCode == http.StatusOK {
					break
				}
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

	// With --proxy-server, Chromium routes ALL HTTPS through the proxy. An
	// untrusted CA causes Chromium to reject every TLS connection - Desktop
	// cannot reach api.anthropic.com at all. Fail fast rather than launch a
	// broken instance.
	if _, err := os.Stat(caPath); err != nil {
		return fmt.Errorf("proxy CA not found at %s - start the proxy first, then run: sudo agentsaegis trust-cert", caPath)
	}
	if !isCATrusted(caPath) {
		return fmt.Errorf("proxy CA is not trusted by the system - run: sudo agentsaegis trust-cert")
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

// isCATrusted checks whether the given CA cert is trusted by the system.
// On macOS uses `security verify-cert`. On Linux checks if the cert is
// installed in the system CA bundle. Returns true on other platforms
// (no verification available).
func isCATrusted(caPath string) bool {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("security", "verify-cert", "-c", caPath).Run() == nil // nosemgrep: dangerous-exec-command -- caPath from config dir
	case "linux":
		_, err := os.Stat("/usr/local/share/ca-certificates/agentsaegis.crt")
		return err == nil
	default:
		return true
	}
}
