# Contributing to Cloak

Thanks for helping harden the local trust layer for cloud AI.

## Dev setup

```bash
git clone https://github.com/PrateekKumar1709/cloak.git
cd cloak
make test
make build
./bin/cloak start --no-lemonade
```

Requires Go 1.25+.

## Guidelines

- Keep packages small: `proxy`, `detect`, `entmap`, `policy`, `lemonade`, `audit`, `web`.
- Never log raw entity values at info level; audit UI is reveal-on-click.
- Streaming paths must preserve SSE framing; only transform text deltas.
- Add property-based / chunk-boundary tests for rehydration changes.
- Conventional commits preferred (`feat:`, `fix:`, `docs:`).

## PR checklist

- [ ] `go test ./...` passes
- [ ] `go vet ./...` clean
- [ ] Docs updated if UX/config changed
