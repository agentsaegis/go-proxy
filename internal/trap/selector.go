package trap

import (
	"math/rand"
	"strings"
	"sync"
	"time"
)

// Selector picks which trap template to use based on the original command context.
type Selector struct {
	mu          sync.Mutex
	templates   []*Template
	recentTraps map[string]time.Time
	rng         *rand.Rand
}

// NewSelector creates a new trap selector with the loaded templates.
func NewSelector(templates []*Template) *Selector {
	return &Selector{
		templates:   templates,
		recentTraps: make(map[string]time.Time),
		rng:         rand.New(rand.NewSource(time.Now().UnixNano())),
	}
}

// SelectTrap picks a trap template appropriate for the given original command.
func (s *Selector) SelectTrap(originalCommand string) *Template {
	s.mu.Lock()
	defer s.mu.Unlock()

	keywords := extractKeywords(originalCommand)

	candidates := s.matchTemplates(keywords)

	if len(candidates) == 0 {
		candidates = s.generalTraps()
	}

	candidates = s.filterRecent(candidates)

	if len(candidates) == 0 {
		// All traps recently used, reset and try again
		s.recentTraps = make(map[string]time.Time)
		candidates = s.templates
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

func (s *Selector) matchTemplates(keywords []string) []*Template {
	var matches []*Template
	for _, t := range s.templates {
		if t.matchesKeywords(keywords) {
			matches = append(matches, t)
		}
	}
	return matches
}

func (s *Selector) generalTraps() []*Template {
	// Return traps with broad triggers
	var general []*Template
	for _, t := range s.templates {
		if t.Category == "destructive" || t.Category == "exfiltration" {
			general = append(general, t)
		}
	}
	return general
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
