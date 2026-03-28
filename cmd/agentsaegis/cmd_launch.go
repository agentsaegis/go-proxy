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
	Long:  "Starts the proxy if needed, then launches Claude Desktop with ANTHROPIC_BASE_URL pointing to the proxy.",
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

	// Preflight: verify the proxy CA is trusted by the system.
	// Without this, the MITM TLS handshake will fail and Desktop
	// won't be able to reach the Anthropic API through the proxy.
	if !isCATrusted(caPath) {
		return fmt.Errorf("proxy CA is not trusted by the system.\nRun: sudo agentsaegis trust-cert")
	}

	cmd := exec.Command(appPath)
	cmd.Env = append(os.Environ(),
		fmt.Sprintf("HTTPS_PROXY=%s", proxyURL),
		fmt.Sprintf("NODE_EXTRA_CA_CERTS=%s", caPath),
	)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("launching Claude Desktop: %w", err)
	}

	fmt.Printf("Claude Desktop launched with HTTPS_PROXY=%s NODE_EXTRA_CA_CERTS=%s\n", proxyURL, caPath)
	return nil
}

// isCATrusted checks whether the proxy CA certificate is trusted by the system.
func isCATrusted(caPath string) bool {
	if _, err := os.Stat(caPath); err != nil {
		return false
	}
	switch runtime.GOOS {
	case "darwin":
		err := exec.Command("security", "verify-cert", "-c", caPath).Run()
		return err == nil
	case "linux":
		// Check if the cert is installed in the system CA bundle
		_, err := os.Stat("/usr/local/share/ca-certificates/agentsaegis.crt")
		return err == nil
	default:
		return false
	}
}
