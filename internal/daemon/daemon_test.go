package daemon

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

func TestPIDFile(t *testing.T) {
	dir := t.TempDir()
	got := PIDFile(dir)
	want := filepath.Join(dir, "aegis.pid")
	if got != want {
		t.Errorf("PIDFile() = %q, want %q", got, want)
	}
}

func TestLogFile(t *testing.T) {
	dir := t.TempDir()
	got := LogFile(dir)
	want := filepath.Join(dir, "aegis.log")
	if got != want {
		t.Errorf("LogFile() = %q, want %q", got, want)
	}
}

func TestWriteAndReadPID(t *testing.T) {
	dir := t.TempDir()

	err := WritePID(dir)
	if err != nil {
		t.Fatalf("WritePID() error = %v", err)
	}

	pid, err := ReadPID(dir)
	if err != nil {
		t.Fatalf("ReadPID() error = %v", err)
	}

	if pid != os.Getpid() {
		t.Errorf("ReadPID() = %d, want %d", pid, os.Getpid())
	}
}

func TestReadPID_FileNotExist(t *testing.T) {
	dir := t.TempDir()

	_, err := ReadPID(dir)
	if err == nil {
		t.Fatal("ReadPID() expected error for missing file, got nil")
	}
}

func TestReadPID_InvalidContent(t *testing.T) {
	dir := t.TempDir()
	pidPath := PIDFile(dir)

	if err := os.WriteFile(pidPath, []byte("not-a-number"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	_, err := ReadPID(dir)
	if err == nil {
		t.Fatal("ReadPID() expected error for invalid content, got nil")
	}
}

func TestReadPID_WithWhitespace(t *testing.T) {
	dir := t.TempDir()
	pidPath := PIDFile(dir)

	if err := os.WriteFile(pidPath, []byte("  12345  \n"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	pid, err := ReadPID(dir)
	if err != nil {
		t.Fatalf("ReadPID() error = %v", err)
	}
	if pid != 12345 {
		t.Errorf("ReadPID() = %d, want %d", pid, 12345)
	}
}

func TestRemovePID(t *testing.T) {
	dir := t.TempDir()

	if err := WritePID(dir); err != nil {
		t.Fatalf("WritePID() error = %v", err)
	}

	if err := RemovePID(dir); err != nil {
		t.Fatalf("RemovePID() error = %v", err)
	}

	pidPath := PIDFile(dir)
	if _, err := os.Stat(pidPath); !os.IsNotExist(err) {
		t.Errorf("PID file should have been removed, stat error = %v", err)
	}
}

func TestRemovePID_NoFile(t *testing.T) {
	dir := t.TempDir()

	err := RemovePID(dir)
	if err == nil {
		t.Fatal("RemovePID() expected error for missing file, got nil")
	}
}

func TestIsRunning_CurrentProcess(t *testing.T) {
	if !IsRunning(os.Getpid()) {
		t.Error("IsRunning(os.Getpid()) = false, want true")
	}
}

func TestIsRunning_InvalidPID(t *testing.T) {
	// PID 0 is special; use a very large PID that almost certainly doesn't exist
	if IsRunning(9999999) {
		t.Error("IsRunning(9999999) = true, want false")
	}
}

func TestWritePID_FileContents(t *testing.T) {
	dir := t.TempDir()

	if err := WritePID(dir); err != nil {
		t.Fatalf("WritePID() error = %v", err)
	}

	data, err := os.ReadFile(PIDFile(dir))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}

	want := strconv.Itoa(os.Getpid())
	if string(data) != want {
		t.Errorf("PID file content = %q, want %q", string(data), want)
	}
}
