# Minitest Connector: security overview

A one-page summary for security and network teams reviewing the connector before it runs
inside their network. To report a vulnerability, email support@minitap.ai rather than
opening a public issue.

## What it does

Minitest runs your app on test devices hosted by Minitap. When the app needs a host that
only exists inside your network (a staging API, an internal web app), the test device's
traffic for that host goes to Minitap, then down a connection that the connector opened
from inside your network, and the connector opens the TCP connection to the host.

```
test device ── Minitap relay ◄══ outbound WSS :443 ══ connector (your machine) ──► allowed host
```

Nothing else changes: traffic to public hosts goes straight to the internet from the
test device, not through your network.

## Network footprint

| | |
|---|---|
| Inbound ports opened on your side | **None** |
| Outbound connection | WSS (TLS 1.2+) to `connect.minitap.ai:443`, two long-lived connections |
| Corporate proxy | Supported through `HTTPS_PROXY` |
| Connections to your hosts | Plain TCP from the connector machine, only to hosts and ports you allow |

## What you control

All of these are enforced **on your machine**, before any connection is opened. Minitap
cannot change them remotely.

- **Allowed hosts** (`--allow`): exact hostnames or `*.subdomain` wildcards. Any other
  host is refused, even if Minitap asks for it.
- **Allowed ports** (`--ports`, default `443`).
- **Blocked addresses**: loopback, link-local, multicast, unspecified and cloud metadata
  addresses (`169.254.169.254`, `fd00:ec2::254`, `100.100.100.200`) are always refused,
  even when an allowed hostname resolves to them, so a DNS answer cannot point the
  connector at its own machine or at cloud credentials. Private ranges (`10.0.0.0/8`,
  `172.16.0.0/12`, `192.168.0.0/16`) stay reachable, since that is where internal hosts
  live: only list hostnames whose DNS you control.
- **DNS**: names are resolved by the connector with your network's resolvers.
- **On/off**: stopping the service or revoking the token cuts access immediately.

## What Minitap controls

- **Tenant isolation.** A connector token belongs to one Minitest workspace. The relay
  only sends a workspace's own test runs to its own connectors.
- **Per-app scope.** Each app lists the internal domains it needs. Every test run gets a
  short-lived signed token (Ed25519, at most 24 hours) carrying the workspace, app,
  connector, domains and ports. The relay refuses any request outside that scope. A host
  must be allowed on both sides to be reachable.
- **Who can manage connectors.** Only workspace owners and admins can create, rename,
  rotate or revoke connectors. Every change is recorded with the user, IP address and
  time, and shown in the connector's activity in the web app.
- **Fail closed.** If the connector is offline, test runs that need it do not start, and a
  run that loses it stops with an explicit "private network lost" error instead of
  testing against the wrong environment.

## Credentials

- The connector token is shown once when the connector is created. Minitap stores only a
  SHA-256 hash of it.
- On your machine, the token sits in a configuration file readable by its owner only
  (`0600`), never in the service definition or the process arguments.
- Tokens can be rotated (the old one stops working at once) or revoked from the web app.

## Data handling

- The connector forwards bytes; it does not inspect, store or modify them. TLS between the
  app and your host is end to end: when your host serves HTTPS, Minitap and the connector
  only see encrypted traffic.
- Connector logs contain hostnames, ports, byte counts and durations. Never tokens,
  headers or payloads.
- Minitap keeps an audit log of connector connections, disconnections, opened and refused
  streams, and configuration changes (who, what, when, from which IP), retained for one
  year.

## Software supply chain

- Open source under the Apache 2.0 license: this repository is the code that runs on your
  machine.
- Releases are built by GitHub Actions from tagged commits, with SHA-256 checksums
  published alongside. `install.sh` verifies the checksum before installing.
- A single static Go binary with no runtime dependencies. On Linux, the system service
  runs as an unprivileged dynamic user with a read-only filesystem.

## Availability

The connector keeps two connections to different relay instances and reconnects with
backoff on its own. For redundancy, run it on two machines with the same token and the
same allow list.
