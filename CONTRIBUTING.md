# Contributing to AgentsAegis

## Prerequisites

- Go 1.22+
- Make

## Development

```bash
git clone https://github.com/agentsaegis/go-proxy.git
cd go-proxy
make build
```

## Testing

Run all checks before submitting a PR:

```bash
make lint
make test
make test-e2e
```

Unit tests require >90% coverage. The E2E suite starts a real proxy with mock backends.

## Submitting changes

1. Fork the repo
2. Create a branch from `main`
3. Make your changes
4. Ensure all tests pass
5. Open a pull request

## Reporting issues

Use [GitHub Issues](https://github.com/agentsaegis/go-proxy/issues). For security vulnerabilities, see [SECURITY.md](SECURITY.md).
