package trap

import (
	"testing"
)

func TestMatchCommand_ExactMatch(t *testing.T) {
	r := MatchCommand("rm -rf .git .env", "rm -rf .git .env")
	if !r.Matched || r.Confidence != 1.0 {
		t.Errorf("exact match: got Matched=%v, Confidence=%v", r.Matched, r.Confidence)
	}
}

func TestMatchCommand_SudoPrefix(t *testing.T) {
	r := MatchCommand("sudo rm -rf .git .env", "rm -rf .git .env")
	if !r.Matched {
		t.Errorf("sudo prefix: got Matched=%v, Reason=%s", r.Matched, r.Reason)
	}
}

func TestMatchCommand_NohupPrefix(t *testing.T) {
	r := MatchCommand("nohup rm -rf .git .env", "rm -rf .git .env")
	if !r.Matched {
		t.Errorf("nohup prefix: got Matched=%v, Reason=%s", r.Matched, r.Reason)
	}
}

func TestMatchCommand_TimePrefix(t *testing.T) {
	r := MatchCommand("time rm -rf .git .env", "rm -rf .git .env")
	if !r.Matched {
		t.Errorf("time prefix: got Matched=%v, Reason=%s", r.Matched, r.Reason)
	}
}

func TestMatchCommand_CommandPrefix(t *testing.T) {
	r := MatchCommand("command rm -rf .git .env", "rm -rf .git .env")
	if !r.Matched {
		t.Errorf("command prefix: got Matched=%v, Reason=%s", r.Matched, r.Reason)
	}
}

func TestMatchCommand_BashC(t *testing.T) {
	r := MatchCommand(`bash -c "rm -rf .git .env"`, "rm -rf .git .env")
	if !r.Matched {
		t.Errorf("bash -c: got Matched=%v, Reason=%s", r.Matched, r.Reason)
	}
}

func TestMatchCommand_ShC(t *testing.T) {
	r := MatchCommand(`sh -c 'rm -rf .git .env'`, "rm -rf .git .env")
	if !r.Matched {
		t.Errorf("sh -c: got Matched=%v, Reason=%s", r.Matched, r.Reason)
	}
}

func TestMatchCommand_EnvPrefix(t *testing.T) {
	r := MatchCommand("env FORCE=1 rm -rf .git .env", "rm -rf .git .env")
	if !r.Matched {
		t.Errorf("env prefix: got Matched=%v, Reason=%s", r.Matched, r.Reason)
	}
}

func TestMatchCommand_InlineEnvVar(t *testing.T) {
	r := MatchCommand("FORCE=1 rm -rf .git .env", "rm -rf .git .env")
	if !r.Matched {
		t.Errorf("inline env var: got Matched=%v, Reason=%s", r.Matched, r.Reason)
	}
}

func TestMatchCommand_BackslashVerb(t *testing.T) {
	r := MatchCommand(`\rm -rf .git .env`, "rm -rf .git .env")
	if !r.Matched {
		t.Errorf("backslash verb: got Matched=%v, Reason=%s", r.Matched, r.Reason)
	}
}

func TestMatchCommand_QuotedVerb(t *testing.T) {
	r := MatchCommand(`"rm" -rf .git .env`, "rm -rf .git .env")
	if !r.Matched {
		t.Errorf("quoted verb: got Matched=%v, Reason=%s", r.Matched, r.Reason)
	}
}

func TestMatchCommand_NoMatch(t *testing.T) {
	r := MatchCommand("ls -la", "rm -rf .git .env")
	if r.Matched {
		t.Errorf("should not match: got Matched=%v", r.Matched)
	}
}

func TestMatchCommand_DifferentArgs(t *testing.T) {
	r := MatchCommand("rm -rf node_modules", "rm -rf .git .env")
	if r.Matched {
		t.Errorf("different args should not match: got Matched=%v, Confidence=%v", r.Matched, r.Confidence)
	}
}

func TestMatchCommand_HighOverlap(t *testing.T) {
	// 3 of 3 non-flag tokens match (rm, .git, .env) plus extra arg
	r := MatchCommand("rm -rf .git .env ./", "rm -rf .git .env")
	if !r.Matched {
		t.Errorf("high overlap should match: got Matched=%v, Confidence=%v, Reason=%s", r.Matched, r.Confidence, r.Reason)
	}
}

func TestMatchCommand_BatchedCommand(t *testing.T) {
	r := MatchCommand("echo hello && rm -rf .git .env", "rm -rf .git .env")
	if !r.Matched {
		t.Errorf("batched command: got Matched=%v, Reason=%s", r.Matched, r.Reason)
	}
}

func TestMatchCommand_PipedCommand(t *testing.T) {
	r := MatchCommand("find . -name '*.tmp' | xargs rm -rf .git .env", "rm -rf .git .env")
	if !r.Matched {
		t.Errorf("piped command: got Matched=%v, Reason=%s", r.Matched, r.Reason)
	}
}

func TestMatchCommand_SemicolonBatched(t *testing.T) {
	r := MatchCommand("cd /tmp; rm -rf .git .env", "rm -rf .git .env")
	if !r.Matched {
		t.Errorf("semicolon batched: got Matched=%v, Reason=%s", r.Matched, r.Reason)
	}
}

func TestMatchCommand_EmptyCommands(t *testing.T) {
	r := MatchCommand("", "rm -rf .git .env")
	if r.Matched {
		t.Error("empty hook command should not match")
	}

	r = MatchCommand("rm -rf .git .env", "")
	if r.Matched {
		t.Error("empty trap command should not match")
	}
}

func TestMatchCommand_NpmTyposquat(t *testing.T) {
	r := MatchCommand("npm install lodahs", "npm install lodahs")
	if !r.Matched {
		t.Errorf("npm typosquat: got Matched=%v", r.Matched)
	}
}

func TestMatchCommand_PipTyposquat(t *testing.T) {
	r := MatchCommand("pip install reqeusts", "pip install reqeusts")
	if !r.Matched {
		t.Errorf("pip typosquat: got Matched=%v", r.Matched)
	}
}

func TestMatchCommand_GitForcePush(t *testing.T) {
	r := MatchCommand("git push --force origin main", "git push --force origin main")
	if !r.Matched {
		t.Errorf("git force push: got Matched=%v", r.Matched)
	}
}

func TestMatchCommand_Chmod777(t *testing.T) {
	r := MatchCommand("chmod 777 /etc/passwd", "chmod 777 /etc/passwd")
	if !r.Matched {
		t.Errorf("chmod 777: got Matched=%v", r.Matched)
	}
}

func TestMatchCommand_BackgroundSuffix(t *testing.T) {
	r := MatchCommand("rm -rf .git .env &", "rm -rf .git .env")
	if !r.Matched {
		t.Errorf("background suffix: got Matched=%v, Reason=%s", r.Matched, r.Reason)
	}
}

func TestNormalizeCommand_Basic(t *testing.T) {
	tokens := NormalizeCommand("rm -rf .git .env")
	expected := []string{"rm", "-rf", ".git", ".env"}
	if !tokensEqual(tokens, expected) {
		t.Errorf("NormalizeCommand basic: got %v, want %v", tokens, expected)
	}
}

func TestNormalizeCommand_MultiplePrefixes(t *testing.T) {
	tokens := NormalizeCommand("sudo nohup time rm -rf .git")
	expected := []string{"rm", "-rf", ".git"}
	if !tokensEqual(tokens, expected) {
		t.Errorf("NormalizeCommand multiple prefixes: got %v, want %v", tokens, expected)
	}
}

func TestNormalizeCommand_Empty(t *testing.T) {
	tokens := NormalizeCommand("")
	if tokens != nil {
		t.Errorf("NormalizeCommand empty: got %v, want nil", tokens)
	}
}

func TestShellSplit_Quotes(t *testing.T) {
	tokens := shellSplit(`echo "hello world" foo`)
	if len(tokens) != 3 || tokens[1] != "hello world" {
		t.Errorf("shellSplit quotes: got %v", tokens)
	}
}

func TestShellSplit_SingleQuotes(t *testing.T) {
	tokens := shellSplit(`echo 'hello world' foo`)
	if len(tokens) != 3 || tokens[1] != "hello world" {
		t.Errorf("shellSplit single quotes: got %v", tokens)
	}
}
