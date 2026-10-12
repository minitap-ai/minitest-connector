# minitest-connector — agent notes

Customer-side agent letting Minitest test devices reach apps inside a customer's private
network. **This repo is public**: no secrets, customer names or internal hostnames in
code, tests, commits or PRs.

The Minitap-side relay lives in the private repo `minitest-connector-relay`, which imports
`pkg/`. See CONTRIBUTING.md for the layout.

## Commands

```sh
go build ./...
go vet ./...
goimports -l .      # must print nothing; CI gates on this
go test -race ./...
golangci-lint run
```

## Conventions

- Standard library `testing` only — no testify, no new deps without need.
- Structured logging via `log/slog` with dotted event names and key-value attrs,
  e.g. `slog.Info("connector.stream_closed", "host", h)`.
- Security first: never log tokens or payloads; hostnames and byte counts only.
  The connector must refuse anything outside its allowlist, and link-local /
  metadata addresses even when a domain resolves there.
- `pkg/` is a public API for the relay: keep it backward compatible with deployed
  connectors; bump `wire.ProtocolVersion` for breaking protocol changes.
- Domain selector syntax must stay identical to `mobile-use-cli/capture/internal/egress`
  (`CanonicalizeSelectors`) so the web app, the connector and the egress agree.
- Comments are sparse; write them only where they prevent a real mistake.
