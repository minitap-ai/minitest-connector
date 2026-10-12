# Minitest Connector

Lets Minitest test devices reach apps and APIs that live inside your private network
(e.g. a staging environment behind a firewall), without opening any inbound port.

You run the connector on a machine inside your network. It opens an outbound-only,
encrypted connection to Minitest, and only the domains you allow go through it.

## How it works

```
Minitest test device ──> Minitest ══ outbound WSS, port 443 ══ connector (your network) ──> your staging
```

- **Outbound only.** The connector dials out over WSS on port 443, like a browser.
  No inbound firewall rule, no VPN. Corporate HTTP proxies are supported (`HTTPS_PROXY`).
- **You decide what is reachable.** The connector only dials hosts that match its
  `--allow` list, on the ports you choose (443 by default). Everything else is refused
  on your machine, before any connection is made.
- **DNS stays inside your network.** Hostnames are resolved by the connector, so
  internal domains work as they do for your team.
- **Defense in depth.** Minitest also restricts each app to the internal domains
  configured for it, and the connector refuses loopback, link-local and cloud metadata
  addresses even when an allowed domain resolves to them.
- **Revocable.** Each connector has its own token, shown once in the Minitest web app
  and revocable at any time. Connections and denied requests are audit-logged.
- **Highly available.** Each connector keeps two connections to Minitest and reconnects
  on its own. For redundancy, run it on two machines with the same token and the same
  `--allow` list.

## Install

Create a connector in the Minitest web app (**Workspace settings → Private network →
Add connector**) to get your token, then install it on a machine that can reach your
staging.

### macOS (Homebrew)

```sh
brew install minitap-ai/tap/minitest-connector
sudo minitest-connector service install \
  --token <token> \
  --allow '*.staging.example.com,api-staging.example.com'
minitest-connector service status
```

### Linux

```sh
curl -fsSL https://get.minitap.ai/connector | sh
sudo minitest-connector service install --token <token> --allow '*.staging.example.com'
```

### Docker

```sh
docker run -d --restart unless-stopped --name minitest-connector \
  -e MINITEST_CONNECTOR_TOKEN=<token> \
  -e MINITEST_CONNECTOR_ALLOW='*.staging.example.com' \
  ghcr.io/minitap-ai/minitest-connector:latest
```

With `sudo`, `service install` registers a service that starts at boot (launchd daemon
on macOS, systemd unit running as an unprivileged dynamic user on Linux). Without
`sudo`, it installs a per-user service. The token is stored in a `0600` config file,
never in the service definition. Remove it with `minitest-connector service uninstall`.

To run it in the foreground instead (e.g. to try it out):

```sh
MINITEST_CONNECTOR_TOKEN=<token> minitest-connector run --allow '*.staging.example.com'
```

## Allowed domains

`--allow` takes a comma-separated list:

| Entry | Matches |
|-------|---------|
| `api-staging.example.com` | exactly that host |
| `*.staging.example.com` | any subdomain of `staging.example.com` (not the domain itself) |

Up to 64 entries. In the Minitest web app, each app lists the internal domains it needs;
a domain must be allowed both there and on the connector to be reachable.

## Configuration

| Flag | Environment variable | Default |
|------|----------------------|---------|
| (token) | `MINITEST_CONNECTOR_TOKEN` | required |
| `--allow` | `MINITEST_CONNECTOR_ALLOW` | required |
| `--ports` | `MINITEST_CONNECTOR_PORTS` | `443` |
| `--config <file>` | | YAML with `token`, `allow`, `ports` |
| | `HTTPS_PROXY` | corporate proxy for the outbound connection |

Logs go to `/Library/Logs/minitest-connector.log` (macOS service),
`journalctl -u minitest-connector` (Linux service), or stdout (`run`, Docker). They
contain hostnames and byte counts, never tokens or payloads.

## Network requirements

The machine needs outbound HTTPS to `connect.minitap.ai` on port 443, and network
access to the internal hosts you allow.

## Security

Found a vulnerability? Please email security@minitap.ai rather than opening an issue.

## Development

See [CONTRIBUTING.md](CONTRIBUTING.md).
