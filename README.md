# minitest-connector

Lets Minitest test devices reach apps and APIs that live inside a customer's private
network (e.g. a staging environment behind a firewall), without opening any inbound port.

Same model as LambdaTest Tunnel or BrowserStack Local: the customer runs a small agent
inside their network, it dials out to Minitap, and only the domains they allow go through.

## Components

| Path | Runs where | What it does |
|------|-----------|--------------|
| `cmd/connector` | Customer network (macOS, Linux, Docker) | Opens an outbound-only WSS connection on port 443 to the relay and forwards streams to internal hosts. Only domains matching its local `--allow` list are dialed; DNS resolves inside the customer network. |
| `cmd/relay` | Minitap GKE | Authenticates connectors (tokens issued by testing-service), keeps one session per connector, and bridges device egress traffic (HTTP CONNECT from the testing-service worker's `limrtc egress`) to the right tenant's connector. |

## Traffic path

```
Limrun device ──Limrun tunnel (domain selectors)──> testing-service worker (limrtc egress)
  ──HTTP CONNECT + run token──> relay (GKE)
  <══ WSS 443 (outbound from customer) ══ connector ──> private network
```

- The customer controls what is reachable (`--allow` on the connector). Minitap enforces
  the app's configured domains a second time on the relay.
- Tokens are shown once, stored hashed by testing-service, revocable from the web app.
- Runs fail closed: if the app's connector is offline, the run is blocked before a device
  is leased.

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
go run ./internal/tools/devtoken -domains example.com > /tmp/pn.env   # dev keys + tokens
set -a; . /tmp/pn.env; set +a
RELAY_PUBLIC_PORT=18080 RELAY_INTERNAL_PORT=18081 go run ./cmd/relay &
go run ./cmd/connector run --relay ws://localhost:18080/v1/connect --allow example.com &
curl -p --proxy http://localhost:18081 \
  --proxy-header "Proxy-Authorization: Bearer $RUN_TOKEN" https://example.com/
```

The connector refuses loopback and link-local targets, so test against a real
hostname.

### Configuration

Connector (`minitest-connector run`): `MINITEST_CONNECTOR_TOKEN` (required),
`--allow` / `MINITEST_CONNECTOR_ALLOW`, `--ports` / `MINITEST_CONNECTOR_PORTS`
(default `443`), `--relay` / `MINITEST_CONNECTOR_RELAY`, `--config <yaml>`,
`HTTPS_PROXY` for corporate proxies.

Relay: `RELAY_RUN_TOKEN_KEYS` (`kid:base64-ed25519-pub,...`), `RELAY_STATIC_CONNECTORS`
(dev only), `RELAY_PUBLIC_PORT` (8080), `RELAY_INTERNAL_PORT` (8081),
`RELAY_PEERS_SERVICE` (headless service for peer lookup), `POD_NAME`, `POD_IP`,
`RELAY_TRUST_PROXY_HEADERS`, `RELAY_DRAIN_TIMEOUT_SECONDS` (1200), `LOG_LEVEL`.

## Release

Connector binaries (darwin arm64/amd64, signed and notarized; linux amd64/arm64), the
Docker image, and the Homebrew formula in `minitap-ai/homebrew-tap` are published from
`v*` tags. The relay image deploys through `k8s-apps` like other Minitap services.

## Related repos

- `mobile-use-cli` (`capture/internal/egress`): Limrun destination tunnel and domain
  selectors, shared with the residential proxy.
- `testing-service`: connector registry, per-app private network config, run gating.
- `webapp-minitest`: Workspace settings → Private network, app settings, run launcher.
