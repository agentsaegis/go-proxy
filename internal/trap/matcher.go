package trap

import (
	"crypto/sha256"
	"fmt"
	"strings"
)

// MatchResult holds the outcome of comparing a hook command to a trap command.
type MatchResult struct {
	Matched    bool
	Confidence float64 // 0.0 to 1.0
	Reason     string  // debug info
}

// MatchCommand checks if hookCmd structurally matches trapCmd.
// It handles shell wrappers (sudo, bash -c, nohup, etc.) and batched commands.
func MatchCommand(hookCmd, trapCmd string) MatchResult {
	if hookCmd == "" || trapCmd == "" {
		return MatchResult{Matched: false, Reason: "empty command"}
	}

	// Fast path: exact SHA256 match
	if commandSHA256(hookCmd) == commandSHA256(trapCmd) {
		return MatchResult{Matched: true, Confidence: 1.0, Reason: "exact hash match"}
	}

	// Normalize both commands
	hookTokens := NormalizeCommand(hookCmd)
	trapTokens := NormalizeCommand(trapCmd)

	if len(hookTokens) == 0 || len(trapTokens) == 0 {
		return MatchResult{Matched: false, Reason: "empty after normalization"}
	}

	// Exact normalized match
	if tokensEqual(hookTokens, trapTokens) {
		return MatchResult{Matched: true, Confidence: 1.0, Reason: "exact normalized match"}
	}

	// Check batched commands: split on &&, ||, ; and check each segment
	hookSegments := splitBatchedCommand(hookCmd)
	for _, seg := range hookSegments {
		segTokens := NormalizeCommand(seg)
		if tokensEqual(segTokens, trapTokens) {
			return MatchResult{Matched: true, Confidence: 1.0, Reason: "segment match in batched command"}
		}
	}

	// Fuzzy token matching: verb must match, then check arg overlap
	if len(hookTokens) > 0 && len(trapTokens) > 0 && hookTokens[0] == trapTokens[0] {
		confidence := tokenOverlap(hookTokens, trapTokens)
		if confidence >= 0.85 {
			return MatchResult{Matched: true, Confidence: confidence, Reason: "fuzzy match: same verb, high arg overlap"}
		}
		return MatchResult{Matched: false, Confidence: confidence, Reason: "same verb but insufficient arg overlap"}
	}

	return MatchResult{Matched: false, Reason: "no match"}
}

// NormalizeCommand strips shell prefixes and returns the core command tokens.
// Strips: sudo, nohup, time, env VAR=val, bash -c "...", command, backslash prefix, quoted verbs.
func NormalizeCommand(cmd string) []string {
	cmd = strings.TrimSpace(cmd)
	if cmd == "" {
		return nil
	}

	// Handle bash -c "..." or bash -c '...' - extract the inner command
	if inner := extractBashC(cmd); inner != "" {
		return NormalizeCommand(inner)
	}

	tokens := shellSplit(cmd)
	if len(tokens) == 0 {
		return nil
	}

	// Strip prefix commands
	for len(tokens) > 0 {
		t := tokens[0]
		switch {
		case t == "sudo", t == "nohup", t == "time", t == "command", t == "xargs":
			tokens = tokens[1:]
		case t == "env" && len(tokens) > 1 && strings.Contains(tokens[1], "="):
			// env VAR=val cmd... - skip env and all VAR=val pairs
			tokens = tokens[1:]
			for len(tokens) > 0 && strings.Contains(tokens[0], "=") {
				tokens = tokens[1:]
			}
		case strings.Contains(t, "=") && !strings.HasPrefix(t, "-"):
			// Inline VAR=val before command
			tokens = tokens[1:]
		default:
			goto done
		}
	}
done:

	if len(tokens) == 0 {
		return nil
	}

	// Strip backslash prefix from verb (e.g. \rm -> rm)
	tokens[0] = strings.TrimPrefix(tokens[0], `\`)

	// Strip quotes from verb (e.g. "rm" -> rm, 'rm' -> rm)
	tokens[0] = stripQuotes(tokens[0])

	// Strip trailing & (backgrounding)
	if len(tokens) > 0 && tokens[len(tokens)-1] == "&" {
		tokens = tokens[:len(tokens)-1]
	}

	return tokens
}

// extractBashC checks if cmd starts with bash -c or sh -c and extracts the inner command.
func extractBashC(cmd string) string {
	for _, shell := range []string{"bash", "sh", "/bin/bash", "/bin/sh"} {
		prefix := shell + " -c "
		if strings.HasPrefix(cmd, prefix) {
			inner := strings.TrimPrefix(cmd, prefix)
			return stripQuotes(strings.TrimSpace(inner))
		}
	}
	return ""
}

// shellSplit splits a command string into tokens, respecting quotes.
func shellSplit(cmd string) []string {
	var tokens []string
	var current strings.Builder
	inSingle := false
	inDouble := false
	escaped := false

	for _, r := range cmd {
		if escaped {
			current.WriteRune(r)
			escaped = false
			continue
		}
		if r == '\\' && !inSingle {
			escaped = true
			continue
		}
		if r == '\'' && !inDouble {
			inSingle = !inSingle
			continue
		}
		if r == '"' && !inSingle {
			inDouble = !inDouble
			continue
		}
		if (r == ' ' || r == '\t' || r == '\n') && !inSingle && !inDouble {
			if current.Len() > 0 {
				tokens = append(tokens, current.String())
				current.Reset()
			}
			continue
		}
		current.WriteRune(r)
	}
	if current.Len() > 0 {
		tokens = append(tokens, current.String())
	}
	return tokens
}

// splitBatchedCommand splits on &&, ||, ; and | to get individual command segments.
func splitBatchedCommand(cmd string) []string {
	// Replace multi-char separators first
	s := strings.ReplaceAll(cmd, "&&", "\x00")
	s = strings.ReplaceAll(s, "||", "\x00")
	s = strings.ReplaceAll(s, "|", "\x00")
	parts := strings.Split(s, "\x00")

	// Also split on ;
	var result []string
	for _, p := range parts {
		for _, seg := range strings.Split(p, ";") {
			seg = strings.TrimSpace(seg)
			if seg != "" {
				result = append(result, seg)
			}
		}
	}
	return result
}

// stripQuotes removes surrounding single or double quotes from a string.
func stripQuotes(s string) string {
	if len(s) >= 2 {
		if (s[0] == '\'' && s[len(s)-1] == '\'') || (s[0] == '"' && s[len(s)-1] == '"') {
			return s[1 : len(s)-1]
		}
	}
	return s
}

// tokensEqual checks if two token slices are identical.
func tokensEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// tokenOverlap computes the overlap ratio between two token slices.
// It compares the verb and counts matching args.
func tokenOverlap(hook, trap []string) float64 {
	if len(trap) == 0 {
		return 0
	}

	// Verb must match (already checked by caller)
	matches := 1 // verb
	for _, t := range trap[1:] {
		for _, h := range hook[1:] {
			if h == t {
				matches++
				break
			}
		}
	}

	return float64(matches) / float64(len(trap))
}

// commandSHA256 returns the hex SHA256 of a command string.
func commandSHA256(cmd string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(cmd)))
}
