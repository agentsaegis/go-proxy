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
	Long:  "Starts the proxy if needed, then launches Claude Desktop with ANTHROPIC_BASE_URL for API interception and --proxy-server for Chromium traffic routing. Requires the proxy CA to be trusted (run: sudo agentsaegis trust-cert).",
	RunE:  runLaunch,
}

func init() {
	// TODO: Claude Desktop MITM via --proxy-server is not working reliably
	// (Chromium rejects the proxy CA despite system trust). Disabled until fixed.
	// rootCmd.AddCommand(launchCmd)
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
		_ = resp.Body.Close()
	}
	if err != nil || resp.StatusCode != http.StatusOK {
		// Start proxy as daemon
		fmt.Println("Starting proxy daemon...")
		exe, exeErr := os.Executable()
		if exeErr != nil {
			return fmt.Errorf("finding executable: %w", exeErr)
		}
		startCmd := exec.Command(exe, "start", "--daemon") // nosemgrep: dangerous-exec-command -- exe from os.Executable()
		startCmd.Stdout = os.Stdout
		startCmd.Stderr = os.Stderr
		if startErr := startCmd.Run(); startErr != nil {
			return fmt.Errorf("starting proxy: %w", startErr)
		}
		// Wait for health
		for i := 0; i < 6; i++ {
			time.Sleep(500 * time.Millisecond)
			if r, e := client.Get(healthURL); e == nil {
				_ = r.Body.Close()
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

	// Verify CA is trusted (needed for --proxy-server Chromium TLS MITM).
	if _, err := os.Stat(caPath); err != nil {
		return fmt.Errorf("proxy CA not found at %s - start the proxy first, then run: sudo agentsaegis trust-cert", caPath)
	}
	if !isCATrusted(caPath) {
		return fmt.Errorf("proxy CA is not trusted by the system - run: sudo agentsaegis trust-cert")
	}

	// --proxy-server: routes Chromium HTTPS traffic through the proxy (TLS MITM).
	// ANTHROPIC_BASE_URL: makes Desktop's embedded Anthropic SDK send API
	//   calls to the proxy as plain HTTP (no TLS needed for API interception).
	// NODE_EXTRA_CA_CERTS: lets Node.js trust the proxy CA for any remaining
	//   HTTPS requests in the main process.
	cmd := exec.Command(appPath, // nosemgrep: dangerous-exec-command -- appPath is a hardcoded constant
		fmt.Sprintf("--proxy-server=%s", proxyURL),
	)
	cmd.Env = append(os.Environ(),
		fmt.Sprintf("ANTHROPIC_BASE_URL=%s", proxyURL),
		fmt.Sprintf("NODE_EXTRA_CA_CERTS=%s", caPath),
	)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("launching Claude Desktop: %w", err)
	}

	fmt.Printf("Claude Desktop launched with ANTHROPIC_BASE_URL=%s --proxy-server=%s\n", proxyURL, proxyURL)
	return nil
}

// isCATrusted checks whether the given CA cert is trusted by the system.
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
