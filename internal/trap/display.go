package trap

import (
	"fmt"
	"io"
	"strings"
)

const (
	colorReset  = "\033[0m"
	colorRed    = "\033[31m"
	colorGreen  = "\033[32m"
	colorYellow = "\033[33m"
	colorCyan   = "\033[36m"
	colorBold   = "\033[1m"
	colorDim    = "\033[2m"
)

// DisplayTrainingMessage renders the ANSI-colored training message to the given writer.
func DisplayTrainingMessage(w io.Writer, trap *ActiveTrap, template *Template, catchRate, teamAverage string) {
	severityColor := colorYellow
	if template.Severity == "critical" {
		severityColor = colorRed
	}

	lines := []string{
		"",
		colorDim + "┌──────────────────────────────────────────────────────────────┐" + colorReset,
		colorDim + "│" + colorReset + "  " + colorYellow + colorBold + "AGENTSAEGIS - Security Awareness Test" + colorReset + strings.Repeat(" ", 22) + colorDim + "│" + colorReset,
		colorDim + "│" + colorReset + strings.Repeat(" ", 62) + colorDim + "│" + colorReset,
		colorDim + "│" + colorReset + "  " + colorRed + "You approved a dangerous command without catching it." + colorReset + "       " + colorDim + "│" + colorReset,
		colorDim + "│" + colorReset + strings.Repeat(" ", 62) + colorDim + "│" + colorReset,
		colorDim + "│" + colorReset + "  Command:  " + colorCyan + truncate(trap.TrapCommand, 48) + colorReset + strings.Repeat(" ", max(0, 48-len(truncate(trap.TrapCommand, 48)))) + colorDim + "  │" + colorReset,
		colorDim + "│" + colorReset + "  Category: " + template.Category + strings.Repeat(" ", max(0, 50-len(template.Category))) + colorDim + "│" + colorReset,
		colorDim + "│" + colorReset + "  Severity: " + severityColor + strings.ToUpper(template.Severity) + colorReset + strings.Repeat(" ", max(0, 50-len(template.Severity))) + colorDim + "│" + colorReset,
		colorDim + "│" + colorReset + strings.Repeat(" ", 62) + colorDim + "│" + colorReset,
		colorDim + "│" + colorReset + "  Risk: " + truncate(template.Training.Risk, 54) + strings.Repeat(" ", max(0, 54-len(truncate(template.Training.Risk, 54)))) + colorDim + "│" + colorReset,
		colorDim + "│" + colorReset + strings.Repeat(" ", 62) + colorDim + "│" + colorReset,
	}

	if len(template.Training.RedFlags) > 0 {
		lines = append(lines, colorDim+"│"+colorReset+"  Red flags you missed:"+strings.Repeat(" ", 39)+colorDim+"│"+colorReset)
		for _, flag := range template.Training.RedFlags {
			flagText := truncate(flag, 54)
			padding := max(0, 56-len(flagText))
			lines = append(lines, colorDim+"│"+colorReset+"    - "+flagText+strings.Repeat(" ", padding)+colorDim+"│"+colorReset)
		}
		lines = append(lines, colorDim+"│"+colorReset+strings.Repeat(" ", 62)+colorDim+"│"+colorReset)
	}

	lines = append(lines,
		colorDim+"│"+colorReset+"  Your catch rate: "+catchRate+strings.Repeat(" ", max(0, 43-len(catchRate)))+colorDim+"│"+colorReset,
		colorDim+"│"+colorReset+"  Team average: "+teamAverage+strings.Repeat(" ", max(0, 46-len(teamAverage)))+colorDim+"│"+colorReset,
		colorDim+"│"+colorReset+strings.Repeat(" ", 62)+colorDim+"│"+colorReset,
		colorDim+"│"+colorReset+"  "+colorGreen+"The command was NOT executed. Your session continues."+colorReset+"       "+colorDim+"│"+colorReset,
		colorDim+"│"+colorReset+strings.Repeat(" ", 62)+colorDim+"│"+colorReset,
		colorDim+"└──────────────────────────────────────────────────────────────┘"+colorReset,
		"",
	)

	for _, line := range lines {
		fmt.Fprintln(w, line)
	}
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}
