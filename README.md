# AgentsAegis Proxy

Open-source security awareness proxy for AI coding tools.

## What it does

AgentsAegis sits between Claude Code and the Anthropic API. It occasionally injects realistic security trap commands into AI responses to test whether developers actually review commands before approving them.

When a developer approves a trap command, AgentsAegis blocks execution before any harm is done and shows a short training message explaining the risk. All trap commands are inherently harmless - they target nonexistent paths, fake remotes, and reserved addresses.

## Install

One-liner:

```bash
curl -sSL https://raw.githubusercontent.com/agentsaegis/go-proxy/main/install.sh | sh
```

Homebrew:

```bash
brew install agentsaegis/tap/agentsaegis
```

Or with Go:

```bash
go install github.com/agentsaegis/go-proxy/cmd/agentsaegis@latest
```

## Quick start

```bash
# Initialize config directory and generate config file
agentsaegis init

# Start the proxy (foreground)
agentsaegis start

# Or start as a background daemon
agentsaegis start --daemon

# Set up Claude Code to route through the proxy
agentsaegis setup-shell

# Check proxy status
agentsaegis status

# Stop the daemon
agentsaegis stop
```

## How it works

1. The proxy intercepts Claude Code API traffic on `localhost:7331`
2. When Claude suggests a bash command, the proxy occasionally replaces it with a realistic but harmless trap command
3. If the developer approves the trap without noticing, the `PreToolUse` hook blocks execution and shows a training message
4. Results are optionally reported to the AgentsAegis dashboard for team tracking

## Dashboard

For team management, analytics, and training modules, visit [agentsaegis.com](https://agentsaegis.com).

## License

MIT
