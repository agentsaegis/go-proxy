package trap

import (
	"math/rand"
	"strings"
	"sync"
	"time"
)

// Selector picks which trap template to use based on the original command context.
type Selector struct {
	mu                sync.Mutex
	templates         []*Template
	allowedCategories map[string]bool // nil or empty = all categories allowed
	allowedSeverities map[string]bool // nil or empty = all severities allowed
	recentTraps       map[string]time.Time
	rng               *rand.Rand
}

// NewSelector creates a new trap selector with the loaded templates.
func NewSelector(templates []*Template) *Selector {
	return &Selector{
		templates:   templates,
		recentTraps: make(map[string]time.Time),
		rng:         rand.New(rand.NewSource(time.Now().UnixNano())),
	}
}

// SetAllowedCategories restricts template selection to the given categories.
// An empty or nil slice allows all categories.
func (s *Selector) SetAllowedCategories(categories []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(categories) == 0 {
		s.allowedCategories = nil
		return
	}
	s.allowedCategories = make(map[string]bool, len(categories))
	for _, c := range categories {
		s.allowedCategories[c] = true
	}
}

// SetDifficulty filters templates by severity based on the org difficulty setting.
// "easy" = only critical/high severity (obvious traps, easier to spot).
// "hard" = only low/medium severity (subtle traps, harder to spot).
// "medium" or empty = all severities.
func (s *Selector) SetDifficulty(difficulty string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch strings.ToLower(difficulty) {
	case "easy":
		s.allowedSeverities = map[string]bool{"critical": true, "high": true}
	case "hard":
		s.allowedSeverities = map[string]bool{"low": true, "medium": true}
	default:
		s.allowedSeverities = nil
	}
}

// SelectTrap picks a trap template appropriate for the given original command.
func (s *Selector) SelectTrap(originalCommand string) *Template {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Filter by allowed categories and severity/difficulty
	eligible := s.templates
	if len(s.allowedCategories) > 0 {
		eligible = s.filterByCategory(eligible)
	}
	if len(s.allowedSeverities) > 0 {
		eligible = s.filterBySeverity(eligible)
	}
	if len(eligible) == 0 {
		return nil
	}

	keywords := extractKeywords(originalCommand)

	candidates := s.matchTemplatesFrom(eligible, keywords)

	if len(candidates) == 0 {
		candidates = s.generalTrapsFrom(eligible)
	}

	candidates = s.filterRecent(candidates)

	if len(candidates) == 0 {
		// All traps recently used, reset and try again
		s.recentTraps = make(map[string]time.Time)
		candidates = eligible
	}

	if len(candidates) == 0 {
		return nil
	}

	return candidates[s.rng.Intn(len(candidates))]
}

// MarkUsed records that a trap was recently shown.
func (s *Selector) MarkUsed(templateID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.recentTraps[templateID] = time.Now()
}

func extractKeywords(command string) []string {
	// Split on whitespace, pipes, semicolons, and common separators
	parts := strings.FieldsFunc(command, func(r rune) bool {
		return r == ' ' || r == '|' || r == ';' || r == '&' || r == '\t' || r == '\n'
	})

	keywords := make([]string, 0, len(parts))
	for _, part := range parts {
		lower := strings.ToLower(strings.TrimSpace(part))
		if lower != "" && len(lower) > 1 {
			keywords = append(keywords, lower)
		}
	}
	return keywords
}

func (s *Selector) matchTemplatesFrom(templates []*Template, keywords []string) []*Template {
	var matches []*Template
	for _, t := range templates {
		if t.matchesKeywords(keywords) {
			matches = append(matches, t)
		}
	}
	return matches
}

func (s *Selector) generalTrapsFrom(templates []*Template) []*Template {
	// Return traps with broad triggers
	var general []*Template
	for _, t := range templates {
		if t.Category == "destructive" || t.Category == "exfiltration" {
			general = append(general, t)
		}
	}
	return general
}

func (s *Selector) filterByCategory(templates []*Template) []*Template {
	var filtered []*Template
	for _, t := range templates {
		if s.allowedCategories[t.Category] {
			filtered = append(filtered, t)
		}
	}
	return filtered
}

func (s *Selector) filterBySeverity(templates []*Template) []*Template {
	var filtered []*Template
	for _, t := range templates {
		if s.allowedSeverities[t.Severity] {
			filtered = append(filtered, t)
		}
	}
	return filtered
}

func (s *Selector) filterRecent(templates []*Template) []*Template {
	cutoff := time.Now().Add(-1 * time.Hour)
	var filtered []*Template
	for _, t := range templates {
		if lastUsed, ok := s.recentTraps[t.ID]; !ok || lastUsed.Before(cutoff) {
			filtered = append(filtered, t)
		}
	}
	return filtered
}
