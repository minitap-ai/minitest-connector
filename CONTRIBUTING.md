# Contributing

This module builds two binaries:

| Path | Runs where | What it does |
|------|-----------|--------------|
| `cmd/connector` | Your network (macOS, Linux, Docker) | Outbound WSS connection to the relay; dials allowed internal hosts. |
| `cmd/relay` | Minitest infrastructure | Authenticates connectors and bridges device traffic (HTTP CONNECT with a short-lived, per-run token) to the right connector. |

```
test device ──> Minitest egress ──HTTP CONNECT + run token──> relay
  <══ WSS 443 (outbound) ══ connector ──> private network
```

## Development

Requires Go (see `go.mod`).

```sh
go build ./...
go vet ./...
goimports -l .        # must print nothing
go test -race ./...
golangci-lint run
```

### Run it locally

```sh
env_file=$(mktemp)   # private file: it holds dev tokens
go run ./internal/tools/devtoken -domains example.com > "$env_file"
set -a; . "$env_file"; set +a
RELAY_PUBLIC_PORT=18080 RELAY_INTERNAL_PORT=18081 go run ./cmd/relay &
go run ./cmd/connector run --relay ws://localhost:18080/v1/connect --allow example.com &
curl -p --proxy http://localhost:18081 \
  --proxy-header "Proxy-Authorization: Bearer $RUN_TOKEN" https://example.com/
```

The connector refuses loopback and link-local targets, so test against a real hostname.

### Relay configuration

`RELAY_RUN_TOKEN_KEYS` (`kid:base64-ed25519-pub,...`), `RELAY_STATIC_CONNECTORS`
(dev only), `RELAY_PUBLIC_PORT` (8080), `RELAY_INTERNAL_PORT` (8081),
`RELAY_PEERS_SERVICE` (headless service for peer lookup), `POD_NAME`, `POD_IP`,
`RELAY_TRUST_PROXY_HEADERS`, `RELAY_DRAIN_TIMEOUT_SECONDS` (1200), `LOG_LEVEL`.

## Release

Pushing a `v*` tag publishes the connector binaries (darwin and linux, amd64 and
arm64), the Docker image, and the Homebrew formula.
