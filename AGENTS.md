# minitest-connector — agent notes

Outbound-only tunnel letting Minitest test devices reach apps inside a customer's private network. This repo is public: no secrets, customer names or internal hostnames in code, tests, commits or PRs.
Two binaries share this module: the connector (customer side) and the relay (GKE).
See README.md for the traffic path.

## Layout

- `cmd/connector` — customer-side agent: WSS to the relay, local domain allowlist,
  dial-out inside the customer network, service install (launchd/systemd)
- `cmd/relay` — Minitap-side: connector sessions, HTTP CONNECT entry for device egress,
  token validation against testing-service
- `internal/version` — build version, injected with `-ldflags "-X .../internal/version.Version=..."`

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
  e.g. `slog.Info("connector.session_opened", "connector_id", id)`.
- Security first: never log tokens or payloads; hostnames and byte counts only.
  The connector must refuse anything outside its allowlist, and link-local /
  metadata addresses even when a domain resolves there.
- Domain selector syntax must stay identical to `mobile-use-cli/capture/internal/egress`
  (`CanonicalizeSelectors`) so the web app, the connector and the egress agree.
- Comments are sparse; write them only where they prevent a real mistake.
