package trap

import (
	"embed"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// TrapsFS holds the embedded trap template YAML files.
//
//go:embed all:traps
var TrapsFS embed.FS

// Template represents a single trap template loaded from YAML.
type Template struct {
	ID           string   `yaml:"id"`
	Category     string   `yaml:"category"`
	Subcategory  string   `yaml:"subcategory"`
	Severity     string   `yaml:"severity"`
	Name         string   `yaml:"name"`
	Description  string   `yaml:"description"`
	Triggers     Triggers `yaml:"triggers"`
	TrapCommands []string `yaml:"trap_commands"`
	Training     Training `yaml:"training"`
}

// Triggers defines when a trap template is applicable.
type Triggers struct {
	Keywords []string `yaml:"keywords"`
}

// Training holds the educational content shown after a trap is missed.
type Training struct {
	Title      string   `yaml:"title"`
	Risk       string   `yaml:"risk"`
	RealWorld  string   `yaml:"real_world"`
	Lesson     string   `yaml:"lesson"`
	RedFlags   []string `yaml:"red_flags"`
	TimeToRead int      `yaml:"time_to_read"`
}

// matchesKeywords checks if any of the template's trigger keywords match the given keywords.
func (t *Template) matchesKeywords(keywords []string) bool {
	for _, trigger := range t.Triggers.Keywords {
		triggerLower := strings.ToLower(trigger)
		for _, kw := range keywords {
			if strings.Contains(kw, triggerLower) || strings.Contains(triggerLower, kw) {
				return true
			}
		}
	}
	return false
}

// LoadTemplates reads and parses all trap template YAML files from the embedded filesystem.
func LoadTemplates() ([]*Template, error) {
	return LoadTemplatesFromFS(TrapsFS, "traps")
}

// LoadTemplatesFromFS reads and parses trap templates from a given filesystem and root path.
func LoadTemplatesFromFS(fsys fs.FS, root string) ([]*Template, error) {
	var templates []*Template

	err := fs.WalkDir(fsys, root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if d.IsDir() {
			return nil
		}

		ext := filepath.Ext(path)
		if ext != ".yml" && ext != ".yaml" {
			return nil
		}

		data, readErr := fs.ReadFile(fsys, path)
		if readErr != nil {
			return fmt.Errorf("reading %s: %w", path, readErr)
		}

		var t Template
		if parseErr := yaml.Unmarshal(data, &t); parseErr != nil {
			return fmt.Errorf("parsing %s: %w", path, parseErr)
		}

		if validateErr := validateTemplate(&t, path); validateErr != nil {
			return validateErr
		}

		templates = append(templates, &t)
		return nil
	})

	if err != nil {
		return nil, fmt.Errorf("loading trap templates: %w", err)
	}

	if len(templates) == 0 {
		return nil, fmt.Errorf("no trap templates found in %s", root)
	}

	return templates, nil
}

// CanaryTemplate returns a hardcoded debug trap that creates a canary file.
// If the file appears on disk after the test, hook blocking failed.
func CanaryTemplate() *Template {
	return &Template{
		ID:          "debug_canary",
		Category:    "debug_canary",
		Subcategory: "test",
		Severity:    "critical",
		Name:        "Debug canary trap",
		Description: "Creates a canary file to verify hook blocking",
		Triggers:    Triggers{Keywords: []string{"any"}},
		TrapCommands: []string{
			fmt.Sprintf("touch /tmp/.aegis_canary_%d", os.Getpid()),
		},
		Training: Training{
			Title:    "SUPER DEBUG: Canary trap triggered",
			Risk:     "This was a test trap. If /tmp/.aegis_canary_* exists, blocking FAILED.",
			Lesson:   "Check: ls /tmp/.aegis_canary_*",
			RedFlags: []string{"Canary file created means hook did not block"},
		},
	}
}

// unsafePatterns lists patterns that indicate a trap command could cause real
// harm if executed. Each entry has a regex-like description and a check function.
var unsafeChecks = []struct {
	name  string
	check func(cmd string) bool
}{
	{
		"rm targeting CWD/parent/home/root",
		func(cmd string) bool {
			if !strings.Contains(cmd, "rm ") {
				return false
			}
			// Allow rm targeting /tmp/.aegis-trap paths
			if strings.Contains(cmd, "/tmp/.aegis-trap") {
				return false
			}
			for _, danger := range []string{" ./", " ../", " ~/", " /", " ."} {
				if strings.Contains(cmd, "rm"+danger) || strings.Contains(cmd, "rm -rf"+danger) || strings.Contains(cmd, "rm -r"+danger) {
					return true
				}
			}
			return false
		},
	},
	{
		"git push without fake remote",
		func(cmd string) bool {
			if !strings.Contains(cmd, "git push") {
				return false
			}
			return !strings.Contains(cmd, "aegis-nonexistent-remote")
		},
	},
	{
		"chmod targeting real paths",
		func(cmd string) bool {
			if !strings.Contains(cmd, "chmod") {
				return false
			}
			if strings.Contains(cmd, "/tmp/.aegis-trap") {
				return false
			}
			for _, danger := range []string{" .", " /", " ~"} {
				if strings.Contains(cmd, "chmod"+danger) || strings.Contains(cmd, "777"+danger) {
					return true
				}
			}
			return false
		},
	},
	{
		"curl/nc to real domains",
		func(cmd string) bool {
			hasCurl := strings.Contains(cmd, "curl")
			hasNc := strings.Contains(cmd, " nc ") || strings.Contains(cmd, "|nc ") || strings.HasSuffix(cmd, " nc")
			if !hasCurl && !hasNc {
				return false
			}
			// Allow 0.0.0.0 (connection refused) destinations
			if strings.Contains(cmd, "0.0.0.0") {
				return false
			}
			// Check for real domains in curl/nc commands
			if hasCurl && (strings.Contains(cmd, "https://") || strings.Contains(cmd, "http://")) {
				// Only allow http://0.0.0.0
				for _, part := range strings.Fields(cmd) {
					if (strings.HasPrefix(part, "http://") || strings.HasPrefix(part, "https://")) && !strings.Contains(part, "0.0.0.0") {
						return true
					}
				}
			}
			if hasNc && !strings.Contains(cmd, "0.0.0.0") && !strings.Contains(cmd, "/nonexistent/") {
				// nc with real hostname
				return true
			}
			return false
		},
	},
	{
		"npm/pip install without aegis-trap prefix",
		func(cmd string) bool {
			isNpmInstall := strings.Contains(cmd, "npm install") || strings.Contains(cmd, "npm add") || strings.Contains(cmd, "yarn add") || strings.Contains(cmd, "pnpm add")
			isPipInstall := strings.Contains(cmd, "pip install") || strings.Contains(cmd, "pip3 install")
			if !isNpmInstall && !isPipInstall {
				return false
			}
			return !strings.Contains(cmd, "aegis-trap-nonexistent") && !strings.Contains(cmd, "@aegis-trap-nonexistent")
		},
	},
	{
		"docker with real images or root mount",
		func(cmd string) bool {
			if !strings.Contains(cmd, "docker") {
				return false
			}
			// docker run -v /:/mnt (mounting root)
			if strings.Contains(cmd, "-v /:/mnt") || strings.Contains(cmd, "-v /:/") {
				return true
			}
			// docker compose without nonexistent compose file
			if strings.Contains(cmd, "docker compose") && !strings.Contains(cmd, "/nonexistent/") && !strings.Contains(cmd, "aegis-trap") {
				return true
			}
			// docker system prune without filter
			if strings.Contains(cmd, "docker system prune") && !strings.Contains(cmd, "aegis-trap") {
				return true
			}
			// docker run with real images (not aegis-trap-image:nonexistent)
			if strings.Contains(cmd, "docker run") && !strings.Contains(cmd, "aegis-trap-image:nonexistent") {
				return true
			}
			return false
		},
	},
	{
		"database reset without nonexistent schema",
		func(cmd string) bool {
			if !strings.Contains(cmd, "prisma") && !strings.Contains(cmd, "db:reset") {
				return false
			}
			if strings.Contains(cmd, "--force") || strings.Contains(cmd, "--reset") {
				return !strings.Contains(cmd, "/nonexistent/")
			}
			return false
		},
	},
	{
		"git add without --dry-run",
		func(cmd string) bool {
			if !strings.Contains(cmd, "git add") {
				return false
			}
			return !strings.Contains(cmd, "--dry-run")
		},
	},
	{
		"env/printenv to real destinations",
		func(cmd string) bool {
			if !strings.Contains(cmd, "echo") && !strings.Contains(cmd, "node -e") {
				return false
			}
			// Check for real env var names (not AEGIS_TRAP_ prefixed)
			if strings.Contains(cmd, "$DATABASE_URL") || strings.Contains(cmd, "$API_KEY") || strings.Contains(cmd, "process.env)") || strings.Contains(cmd, "process.env,") {
				return true
			}
			return false
		},
	},
	{
		"aws with real bucket names",
		func(cmd string) bool {
			if !strings.Contains(cmd, "aws s3") {
				return false
			}
			return !strings.Contains(cmd, "aegis-trap-nonexistent")
		},
	},
}

// ValidateTrapSafety checks all loaded templates to ensure no trap command
// could cause real harm if executed directly. Returns the subset of safe
// templates and logs warnings for any rejected templates.
func ValidateTrapSafety(templates []*Template, logger *slog.Logger) []*Template {
	var safe []*Template

	for _, tmpl := range templates {
		// Canary template is exempt (debug only)
		if tmpl.Category == "debug_canary" {
			safe = append(safe, tmpl)
			continue
		}

		isSafe := true
		for _, cmd := range tmpl.TrapCommands {
			for _, check := range unsafeChecks {
				if check.check(cmd) {
					logger.Warn("UNSAFE trap command rejected",
						"template_id", tmpl.ID,
						"command", cmd,
						"rule", check.name,
					)
					isSafe = false
					break
				}
			}
			if !isSafe {
				break
			}
		}

		if isSafe {
			safe = append(safe, tmpl)
		}
	}

	if len(safe) < len(templates) {
		logger.Warn("some trap templates were rejected as unsafe",
			"total", len(templates),
			"safe", len(safe),
			"rejected", len(templates)-len(safe),
		)
	}

	return safe
}

func validateTemplate(t *Template, path string) error {
	if t.ID == "" {
		return fmt.Errorf("template %s: missing id", path)
	}
	if t.Category == "" {
		return fmt.Errorf("template %s: missing category", path)
	}
	if t.Severity == "" {
		return fmt.Errorf("template %s: missing severity", path)
	}
	if len(t.TrapCommands) == 0 {
		return fmt.Errorf("template %s: no trap_commands defined", path)
	}
	if len(t.Triggers.Keywords) == 0 {
		return fmt.Errorf("template %s: no trigger keywords defined", path)
	}
	if t.Training.Title == "" {
		return fmt.Errorf("template %s: missing training title", path)
	}
	return nil
}
