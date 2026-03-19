// Package daemon manages the AgentsAegis proxy background process.
package daemon

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// PIDFile returns the path to the daemon PID file.
func PIDFile(configDir string) string {
	return filepath.Join(configDir, "aegis.pid")
}

// LogFile returns the path to the daemon log file.
func LogFile(configDir string) string {
	return filepath.Join(configDir, "aegis.log")
}

// WritePID writes the current process ID to the PID file.
func WritePID(configDir string) error {
	pidPath := PIDFile(configDir)
	return os.WriteFile(pidPath, []byte(strconv.Itoa(os.Getpid())), 0o600)
}

// ReadPID reads the daemon process ID from the PID file.
func ReadPID(configDir string) (int, error) {
	pidPath := PIDFile(configDir)
	data, err := os.ReadFile(pidPath)
	if err != nil {
		return 0, fmt.Errorf("reading PID file: %w", err)
	}

	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0, fmt.Errorf("parsing PID: %w", err)
	}

	return pid, nil
}

// RemovePID removes the PID file.
func RemovePID(configDir string) error {
	return os.Remove(PIDFile(configDir))
}

// IsRunning checks if a process with the given PID is running.
func IsRunning(pid int) bool {
	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}

	// On Unix, FindProcess always succeeds. Send signal 0 to check if process exists.
	err = process.Signal(syscall.Signal(0))
	return err == nil
}
