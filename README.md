# AgentsAegis Proxy

Open-source security awareness proxy for AI coding tools.

## What it does

AgentsAegis sits between Claude Code and the Anthropic API. It occasionally injects realistic security trap commands into AI responses to test whether developers actually review commands before approving them.

When a developer approves a trap command, AgentsAegis blocks execution before any harm is done and shows a short training message explaining the risk. All trap commands are inherently harmless - they target nonexistent paths, fake remotes, and reserved addresses.

## Install

Build from source (requires Go 1.22+):

```bash
go install github.com/agentsaegis/go-proxy/cmd/agentsaegis@latest
```

Or clone and build:

```bash
git clone https://github.com/agentsaegis/go-proxy.git
cd go-proxy
make build
./bin/agentsaegis --help
```

Homebrew coming soon.

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
