package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"

	"github.com/spf13/cobra"

	"github.com/agentsaegis/go-proxy/internal/server"
)

var trustCertCmd = &cobra.Command{
	Use:   "trust-cert",
	Short: "Add the AgentsAegis proxy CA to the system trust store",
	Long: `Adds the AgentsAegis proxy CA certificate to the system trust store.
This is required for HTTPS interception of Copilot CLI traffic.
On macOS, this requires sudo. On Linux, it requires sudo and update-ca-certificates.`,
	RunE: runTrustCert,
}

var untrustCertCmd = &cobra.Command{
	Use:   "untrust-cert",
	Short: "Remove the AgentsAegis proxy CA from the system trust store",
	RunE:  runUntrustCert,
}

func init() {
	rootCmd.AddCommand(trustCertCmd)
	rootCmd.AddCommand(untrustCertCmd)
}

func aegisCAPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("getting home directory: %w", err)
	}
	return filepath.Join(home, ".agentsaegis", "ca.pem"), nil
}

func ensureCA() (string, error) {
	caPath, err := aegisCAPath()
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(caPath); os.IsNotExist(err) {
		// Generate CA
		home, _ := os.UserHomeDir()
		aegisDir := filepath.Join(home, ".agentsaegis")
		if mkdirErr := os.MkdirAll(aegisDir, 0o700); mkdirErr != nil {
			return "", fmt.Errorf("creating aegis directory: %w", mkdirErr)
		}
		caManager := server.NewCAManager(aegisDir)
		if caErr := caManager.EnsureCA(); caErr != nil {
			return "", fmt.Errorf("generating CA: %w", caErr)
		}
	}
	return caPath, nil
}

func runTrustCert(_ *cobra.Command, _ []string) error {
	caPath, err := ensureCA()
	if err != nil {
		return err
	}

	switch runtime.GOOS {
	case "darwin":
		fmt.Printf("Adding CA to system keychain (requires sudo):\n")
		fmt.Printf("  sudo security add-trusted-cert -d -r trustRoot -k /Library/Keychains/System.keychain %s\n\n", caPath)
		cmd := exec.Command("sudo", "security", "add-trusted-cert",
			"-d", "-r", "trustRoot",
			"-k", "/Library/Keychains/System.keychain",
			caPath)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		cmd.Stdin = os.Stdin
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("adding certificate to keychain: %w", err)
		}
		fmt.Println("CA certificate trusted successfully.")
		fmt.Println("Restart your browser and Copilot CLI to pick up the new certificate.")

	case "linux":
		dest := "/usr/local/share/ca-certificates/agentsaegis.crt"
		fmt.Printf("Copying CA to %s and running update-ca-certificates (requires sudo).\n\n", dest)
		copyCmd := exec.Command("sudo", "cp", caPath, dest)
		copyCmd.Stdout = os.Stdout
		copyCmd.Stderr = os.Stderr
		if err := copyCmd.Run(); err != nil {
			return fmt.Errorf("copying CA cert: %w", err)
		}
		updateCmd := exec.Command("sudo", "update-ca-certificates")
		updateCmd.Stdout = os.Stdout
		updateCmd.Stderr = os.Stderr
		if err := updateCmd.Run(); err != nil {
			return fmt.Errorf("running update-ca-certificates: %w", err)
		}
		fmt.Println("CA certificate trusted successfully.")

	default:
		fmt.Printf("Manual trust required on %s.\n", runtime.GOOS)
		fmt.Printf("CA certificate path: %s\n", caPath)
		fmt.Println("Add it to your system's trust store manually.")
	}

	return nil
}

func runUntrustCert(_ *cobra.Command, _ []string) error {
	caPath, err := aegisCAPath()
	if err != nil {
		return err
	}
	if _, err := os.Stat(caPath); os.IsNotExist(err) {
		fmt.Println("No AgentsAegis CA certificate found.")
		return nil
	}

	switch runtime.GOOS {
	case "darwin":
		fmt.Printf("Removing CA from system keychain (requires sudo):\n")
		cmd := exec.Command("sudo", "security", "remove-trusted-cert", "-d", caPath)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		cmd.Stdin = os.Stdin
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("removing certificate from keychain: %w", err)
		}
		fmt.Println("CA certificate removed from system trust store.")

	case "linux":
		dest := "/usr/local/share/ca-certificates/agentsaegis.crt"
		rmCmd := exec.Command("sudo", "rm", "-f", dest)
		rmCmd.Stdout = os.Stdout
		rmCmd.Stderr = os.Stderr
		if err := rmCmd.Run(); err != nil {
			return fmt.Errorf("removing CA cert: %w", err)
		}
		updateCmd := exec.Command("sudo", "update-ca-certificates", "--fresh")
		updateCmd.Stdout = os.Stdout
		updateCmd.Stderr = os.Stderr
		if err := updateCmd.Run(); err != nil {
			return fmt.Errorf("running update-ca-certificates: %w", err)
		}
		fmt.Println("CA certificate removed from system trust store.")

	default:
		fmt.Printf("Manual removal required on %s.\n", runtime.GOOS)
		fmt.Printf("Remove the CA at %s from your system trust store.\n", caPath)
	}

	return nil
}
