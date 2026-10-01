# Build, install and operate GraphWAN

The same binary runs either `graphwan server` or `graphwan agent`. The React UI
is embedded; no Node.js process, external web server or packet relay is required.
Other platform adapters and their restrictions
are described in the linked operation guides; a successful cross-build alone
does not establish native runtime support.

## Build and prepare release binaries

Use the pinned Go version from `.go-version`. Checked-in UI assets allow Go-only
builds. After editing the frontend, use the pinned Node/pnpm versions and run
`pnpm --dir web install --frozen-lockfile` and `pnpm --dir web build` first.

```sh
python3 scripts/cross-build.py --target linux/amd64 --output /tmp/graphwan-builds
python3 scripts/package.py --builds /tmp/graphwan-builds --output /tmp/graphwan-release
```

The output directory must not already exist. Omit `--target` to compile all
advertised architectures across the seven selected systems, or repeat it to
select specific targets. Builds use no CGO; the build manifest records the Git
revision, dirty-worktree flag, toolchain, CPU baselines, binary sizes and hashes.
The binary's `version` command reports the revision and `-dirty` where applicable.
Pass `--version v1.2.3` to embed a release version instead; the manifest still
records the exact source revision and worktree state.

Release preparation verifies every binary's size and SHA-256 against the internal
build manifest, then writes one executable per target:
`graphwan-<version>-<os>-<arch>` (`.exe` on Windows). Output files contain exactly
the compiled binary; no archives, documents, manifests or checksum files are
included. Unix files have executable permissions. This script prepares local
files and does not publish releases.

The [release workflow](../.github/workflows/release.yml) builds and publishes these
binaries automatically when a version tag is pushed. See [release CI](continuous-integration.md)
for tag formats, architecture coverage and release contents.

Download the matching binary and rename it to `graphwan` or `graphwan.exe`.
On Unix, run `chmod +x graphwan` after downloading. Documentation, service examples
and helper scripts remain in the repository at the matching version tag.

Windows Agents also require Wintun. From a checkout of the matching tag, run
`python scripts/fetch-wintun.py --arch amd64 --output <binary-directory>`,
substituting `arm64` or `386` for the matching executable. The script installs
the DLL and its distribution license together. See [Windows operation](windows-operation.md).

## Linux systemd installation

Download and rename the matching Linux binary. Obtain the `deploy/linux` examples
from a checkout of the same tag. The examples target a systemd-based Linux
host with `/dev/net/tun`. The controller and Agent may run separately or together.
Install only the unit and configuration needed on each host. Commands below are
administrator actions on the deployment host, not part of the build process.

```sh
sudo install -m 0755 graphwan /usr/local/bin/graphwan
sudo install -d -m 0700 /etc/graphwan
# Controller host:
sudo install -m 0600 deploy/linux/server.env.example /etc/graphwan/server.env
sudo install -m 0644 deploy/linux/graphwan-server.service /etc/systemd/system/
# Agent host:
sudo install -m 0600 deploy/linux/agent.env.example /etc/graphwan/agent.env
sudo install -m 0644 deploy/linux/graphwan-agent.service /etc/systemd/system/
```

Edit `server.env` before starting: set the listener, the DNS names/IPs clients
will use, and a unique administrator password. This is systemd EnvironmentFile
syntax, not shell syntax; do not use `export` or shell substitutions. Quote values
containing spaces. The example exposes HTTPS on port 8443. Permit the controller
port only where administrators and Agents need access.

```sh
sudo systemctl daemon-reload
sudo systemctl enable --now graphwan-server
sudo journalctl -u graphwan-server -n 50 --no-pager
```

The controller runs as a dynamic unprivileged account. systemd manages its private
`/var/lib/graphwan-server` state directory. After initialization, remove
`GRAPHWAN_ADMIN_PASSWORD` from `server.env`; changing that variable does not reset
the stored password. Copy `/var/lib/graphwan-server/ca.pem` using a trusted channel
to administrative clients and to `/etc/graphwan/ca.pem` on each Agent host. This is
the **public** certificate; keep the controller database and CA private key private.

HTTPS uses an ECDSA P-256 server key for TLS 1.3 signature negotiation. The
persisted controller CA and Agent identities remain Ed25519. Upgrading a controller
that previously served an Ed25519 HTTPS key preserves its CA, password and enrolled
Agents. Some browser certificate verifiers also reject the Ed25519 signatures in
the existing certificate chain. The server-key change addresses TLS negotiation,
not that separate certificate-validation limitation.

In the UI, create a one-time enrollment token. Edit `agent.env` with the exact
HTTPS controller origin, CA file, initial display name and token. Then:

```sh
sudo modprobe tun
sudo systemctl enable --now graphwan-agent
sudo journalctl -u graphwan-agent -n 50 --no-pager
```

After the log reports enrollment and the Agent appears in the UI, remove
`GRAPHWAN_ENROLLMENT_TOKEN` from `agent.env`. Its identity, certificate and cached
configuration persist in `/var/lib/graphwan-agent`; do not reuse that directory
on another machine. The Agent's controller origin cannot be changed while retaining
an identity enrolled against a different origin.

Set `GRAPHWAN_SERVER_TRANSPORT` to `tcp` (default), `websocket`, `grpc` or `wss`
for its first enrollment. Registered Agents ignore bootstrap transport changes
and use their locally persisted Server directory. The CLI equivalent is
`--server-transport`. All carriers use the existing controller port and the same
inner TLS identity; changing this setting needs no new enrollment token. Upgrade
the controller first when adding carriers to an existing installation. See the
[control transport wire contract](control-api.md#control-transport-carriers).

The Agent runs as root with only `CAP_NET_ADMIN` and `CAP_NET_BIND_SERVICE` in its
capability bounding set. Its device policy permits the TUN device and standard
pseudo-devices. It shares the host network namespace so applications can use the
virtual routes; do not enable `PrivateNetwork`, `PrivateDevices` or `PrivateUsers`.
Both units restrict writable state and use restrictive file creation permissions.
See [systemd execution settings](https://www.freedesktop.org/software/systemd/man/latest/systemd.exec.html)
for the service manager's configuration semantics.

In the UI, create Networks, assign fixed node addresses and add Edges. Agents
initially have no virtual topology. Permit the configured peer TCP/UDP ports
(default 24752) and any explicit manual endpoints. For NAT deployments configure
STUN using [NAT operation](nat-operation.md). Control-plane outage preserves cached
forwarding and reconnection, but fresh enrollment/configuration changes need the
controller. Service logs go to the journal; status and traffic appear in the UI.

## Geographic topology view

The network's Drawing switch includes a 3D globe. It uses existing Agent public
endpoints and the same node/edge selection and editing panel as the 2D views.
Node details show public IPs, the located IP, city, country and coordinates.
Location is approximate; unknown locations are listed as unlocated, not placed
at invented coordinates. Expired, private, container, CGNAT and reserved IPs are
excluded. STUN observations take priority, followed by public interface IPs and
manual literal IPs; IPv4 is preferred within each source. DNS-only manual
endpoints are not resolved for geolocation because they can point to proxies.

On demand, the Server queries `https://api.ip.sb/geoip/{IP}` over HTTPS with a
custom GraphWAN User-Agent. IP.SB supports IPv4 and IPv6 and credits MaxMind GeoIP
as its data source. Only advertised public endpoint IPs are sent; no Agent
upgrade or extra Agent telemetry is required. Missing coordinates remain unlocated.

Results are cached for seven days in `<data-dir>/geoip/ip-sb-cache.json`, including
across restarts. Expired successful locations remain usable while refreshing or
when the API is unavailable. Unknown locations are also cached. Requests run in
one background worker at least 650 ms apart (below the documented 100/minute and
5/second limits); failures back off for 15 minutes or a longer `Retry-After`.
Queries do not block controller startup, management HTTP requests or routing.
Every Server replica maintains its own cache outside the replicated state;
replicas sharing a public egress IP also share IP.SB's external rate limit.

The former DB-IP downloads and `GRAPHWAN_GEOIP_DB` override are no longer used.
Existing MMDB files may be removed; they are neither read nor refreshed.
Provider attribution is preserved in the globe, node details and bundled notices.
See https://ip.sb/api/ for service limits and commercial-use plans.

## Upgrade, back up and remove

Download the new binary from the release. Stop the corresponding service before
replacing its binary; stopping the Agent temporarily removes its owned TUN devices
and routes. Install the replacement to `/usr/local/bin/graphwan.new`, then rename
it to `/usr/local/bin/graphwan` on the same filesystem and restart the service.
If both services share the binary, stop and restart both. Keep environment files,
the public CA and state directories. Restarting an enrolled Agent needs no token.
Do not run two instances against one state directory.

For a consistent backup, stop the service and copy its **entire** state directory
and corresponding `/etc/graphwan` files to private storage, preserving permissions.
The controller database contains the authority and topology; the Agent database
contains private identity and durable configuration. Restore each to its original
service path before starting. There is no online-backup command or general
cross-version database downgrade guarantee; retain a stopped-state backup and the
previous binary before upgrading. A rollback restores the matching pair.

To remove a service, disable and stop it, remove its unit, then run
`systemctl daemon-reload`. Retain state for recovery unless deliberately deleting
the deployment. Removing a local Agent does not revoke its controller identity;
revoke it in the UI when decommissioning. Stop all services using the executable
before removing it. No uninstaller silently deletes identities or controller data.

## Other systems

Run the executable with the same CLI flags from an elevated terminal where TUN
setup requires it. Use [macOS](macos-operation.md), [FreeBSD](freebsd-operation.md),
[OpenBSD](openbsd-operation.md), [NetBSD](netbsd-operation.md),
[DragonFly](dragonfly-operation.md) or
[Windows](windows-operation.md) instructions for platform setup. The executable
is not a Windows Service Control Manager service; do not use `sc create` directly
against it. Non-systemd supervisors must preserve the private state directory,
provide the initial environment values and forward termination signals.
