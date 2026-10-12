# Contributing

## Layout

| Path | What it is |
|------|-----------|
| `cmd/connector` | The `minitest-connector` binary: `run`, `service install/uninstall/status`, `version`. |
| `pkg/connector` | The agent: outbound sessions to Minitest, allowlist and port checks, dialing. |
| `pkg/wire` | The connector ↔ Minitest protocol (WebSocket, yamux streams, small JSON frames). |
| `pkg/selector` | Allowed-domain syntax (`host`, `*.domain`). |
| `pkg/netguard` | Refuses loopback, link-local and cloud metadata addresses at dial time. |
| `pkg/pipe` | Bidirectional copy with half-close. |
| `internal/service` | launchd and systemd service install. |

`pkg/` is imported by the Minitest relay, so changes there must stay backward compatible
with connectors already deployed (bump `wire.ProtocolVersion` for breaking changes).

## Development

Requires Go (see `go.mod`).

```sh
go build ./...
go vet ./...
goimports -l .        # must print nothing
go test -race ./...
golangci-lint run
```

`pkg/connector` tests run the connector against an in-process fake relay.

## Release

Pushing a `v*` tag publishes the connector binaries (darwin and linux, amd64 and
arm64), the Docker image, and the Homebrew formula.
