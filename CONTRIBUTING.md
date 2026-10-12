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

Push a `v*` tag (e.g. `git tag v0.1.0 && git push origin v0.1.0`). The `Release` workflow:

- builds darwin and linux binaries (amd64, arm64) with GoReleaser and publishes them, with
  `checksums.txt`, to the GitHub release;
- updates the `minitest-connector` cask in `minitap-ai/homebrew-tap` (skipped for
  pre-release tags like `v0.2.0-rc1`);
- pushes the multi-arch image to `ghcr.io/minitap-ai/minitest-connector`;
- installs the new release with `install.sh` on Linux and macOS and checks the version.

Every PR runs a GoReleaser snapshot build and shellcheck on `install.sh`.
