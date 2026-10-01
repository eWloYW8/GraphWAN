# Service management

GraphWAN includes system service management for both Agent and Server:

- Linux: systemd, OpenRC, or OpenWrt procd + rc.common, running as root.
- Windows: native Service Control Manager integration, running as LocalSystem.
- macOS: system launchd LaunchDaemon, running as root.

No additional wrapper executable is required. Other platforms retain the foreground
commands. Installation and service changes require root/an elevated administrator
terminal. Windows and macOS builds are supported, but native execution has not
been verified in the current Linux development environment.

## Register, install and start an Agent

Keep the GraphWAN executable in a permanent, administrator-controlled location.
On Windows, Agent startup and service installation automatically download a
verified, architecture-matching Wintun DLL if it is missing beside the executable
(see [Windows setup](windows-operation.md), including offline installation).
Installation records the executable's resolved absolute path; it does not copy
the GraphWAN binary.

First enroll using an invitation from the management panel:

```sh
sudo /usr/local/bin/graphwan agent enroll --invitation-file invitation.txt --data-dir /var/lib/graphwan-agent
sudo /usr/local/bin/graphwan agent service install --data-dir /var/lib/graphwan-agent
sudo /usr/local/bin/graphwan agent service start
```

On macOS, the same commands apply; `/var/db/graphwan-agent` is another suitable data
directory. In an elevated Windows PowerShell, for example:

```powershell
& 'C:\Program Files\GraphWAN\graphwan.exe' agent enroll --invitation-file invitation.txt --data-dir 'C:\ProgramData\GraphWAN\agent'
& 'C:\Program Files\GraphWAN\graphwan.exe' agent service install --data-dir 'C:\ProgramData\GraphWAN\agent'
& 'C:\Program Files\GraphWAN\graphwan.exe' agent service start
```

Protect the Windows executable and data directory with ACLs allowing modification
only by administrators and SYSTEM. Unix permission bits do not enforce Windows
ACLs. The installer preserves existing data and permissions.

Use the same data directory for enrollment and installation. The default remains
`./graphwan-agent-data`, resolved relative to the install command's working
directory and saved as an absolute path. Installation requires an existing
`agent.db`; it does not register an Agent or copy an enrollment token into the
service definition. Complete enrollment successfully before installing.

Installation enables startup at boot; use `start` to launch immediately.
An existing service is not overwritten. Stop an existing foreground Agent using
that data directory before starting the service.

## Manage a service

```sh
graphwan agent service status
sudo graphwan agent service stop
sudo graphwan agent service start
sudo graphwan agent service restart
sudo graphwan agent service uninstall
```

Uninstall stops the service and removes its service registration, retaining the
binary, data directory, enrollment identity and network configuration. It does
not remove the Agent from the controller.

Use `--service-name NAME` on **every** service command to manage a nondefault
instance. Each Agent instance needs its own enrolled data directory.
The default service names are `graphwan-agent` and `graphwan-server`.
Services created by these commands must be managed from an administrative context
on macOS so launchctl operates on the system domain.

Use `ACTION --help` for flags. `--data-dir`, `--listen` and `--tls-hosts` apply
to installation; start/restart retain the installed configuration. To change the
configuration, uninstall and reinstall with the same data directory and new flags.
The internal `service run` command is the supervisor entry point.

## Server

Initialize a Server first with the normal `graphwan server` command and
`GRAPHWAN_ADMIN_PASSWORD`, then stop it and install the service using that data:

```sh
sudo graphwan server service install --data-dir /var/lib/graphwan-server --listen '[::]:8443' --tls-hosts server.example.com
sudo graphwan server service start
graphwan server service status
```

The other actions are identical to Agent commands. Installation requires an
existing `controller.db`; finish initial startup successfully first. The existing
password hash and CA are reused, so the service does not need the initial admin
password. Shell environment variables are not copied into installed services.
Configure any optional runtime environment through the native supervisor.

## OpenRC and OpenWrt

The same Agent and Server commands above automatically select the host's service
manager. OpenRC installs `/etc/init.d/NAME` and enables the current runlevel (normally `default`);
OpenWrt installs an rc.common script and enables boot startup in `/etc/rc.d`.
Both run as root and restart unexpected exits after three seconds.

OpenRC requires `openrc-run`, `supervise-daemon`, `start-stop-daemon`,
`rc-service` and `rc-update`. OpenWrt uses its native procd/ubus facilities.
Normal service shutdown allows 30 seconds for GraphWAN to release resources.
Managed Agent and Server updates use a separate, non-respawning helper; the
helper is not enabled at boot and removes its registration when finished.
CLI commands and panel update/download-source options are unchanged.

On OpenWrt, use a writable persistent location for both the executable and data,
such as `/root/graphwan` and `/root/graphwan-agent-data`. Avoid `/tmp` and
`/var` for persistent data; they normally reside in RAM. Allow enough free space
for the staged binary, updater helper and previous-version backup. Agents need
TUN support (usually `kmod-tun`) and any tools required by their configured
gateway modes. Installing the service does not change firewall rules.

## Logs, shutdown and upgrades

On systemd, logs are available through `journalctl -u graphwan-agent` (or the Server
service name). OpenRC writes `/var/log/NAME.log` and `/var/log/NAME.err`;
OpenWrt forwards logs to logd (`logread -e graphwan`). macOS writes `/var/log/NAME.out.log` and
`/var/log/NAME.err.log`. Windows sends structured log text to the Application
event log under the service name.

Stopping a service cancels its worker and waits up to 25 seconds for cleanup.
Unexpected worker exits terminate the process with a failure so the supervisor
can restart it. Linux and Windows use a three-second restart delay; launchd
manages restart throttling on macOS.

To upgrade, stop the service, replace the binary at its installed path, and start
it again. Keep the data directory. Service installation does not automatically
upgrade or migrate existing hand-written system services.

Paths with leading/trailing whitespace or containing control characters, percent
signs, dollar signs or double quotes
are rejected to avoid supervisor expansion. On Unix, backslashes and single quotes
are also rejected; ordinary spaces are supported.

## Managed Agent updates

Agents running under the built-in `graphwan agent service` supervisor can be
updated from **Agents → Update**. Check the latest stable GitHub Release, choose
**Agent downloads from GitHub** or **Server downloads and caches**, then select
**Update and restart**. The panel shows the current version, target version and
update status. Foreground Agents and hand-written services using `agent run` do
not advertise this capability. Existing built-in services gain it when first
started with an updater-capable binary; installation need not be repeated.

The equivalent administrative command is:

```sh
sudo graphwan agent update --data-dir /var/lib/graphwan-agent --source github
sudo graphwan agent update --data-dir /var/lib/graphwan-agent --source server
```

These are alternatives. The command queues one update for the running service;
it is picked up when the Agent is connected to a controller. Inspect the panel
or `update-state.json` in the data directory for completion. The service name and
executable path come from the service receipt, so custom names need no extra flag.
Both methods use the latest stable release of `eWloYW8/GraphWAN`, selecting the
Agent's OS and architecture. Prereleases are excluded. Installed release versions
cannot be downgraded; development Git revisions may be explicitly replaced with
a release. There is no automatic periodic installation.

The Server caches release metadata for five minutes and verified binaries under
`<server-data-dir>/updates`, keyed by SHA-256. Concurrent downloads reuse the same
file; files unused for seven days are pruned when new binaries are downloaded.
Caches survive restarts and are local to each Server replica. Update requests
are replicated with configuration; the Agent persists processed request IDs so
reconnections do not repeat an installation. Requests older than 30 minutes fail
and must be issued again. The Server transfer uses the existing authenticated
control carrier (including TCP, WebSocket, gRPC or WSS), via a separate HTTP request.

Both the Server and Agent verify the binary's size and the SHA-256 digest from
[GitHub's release asset metadata](https://docs.github.com/en/rest/releases/assets).
Missing digests, partial downloads, mismatched platforms and invalid binaries
fail before stopping the Agent. The binary is staged beside the installed
executable. A separate, temporary system service stops the Agent, preserves the
previous executable as `<binary>.previous`, replaces it and restarts the service.
The updater checks that startup remains running for ten seconds; if startup
fails, it restores and starts the previous binary. This is a binary rollback,
not a rollback of database migrations or a guarantee of network connectivity.
The existing registration and configuration data are retained.

The installed directory must be writable by the system service. Give independent
Agent instances separate executable paths when updating them independently.
The temporary helper removes its service registration when done; Windows helper
files are cleaned on the next update because Windows locks the running image.
An interrupted machine-level failure can leave `<binary>.update-lock`; inspect
service state and the `.previous` binary before manually recovering that lock.
Linux replacement, restart and startup-failure rollback have been verified.
Windows/macOS are cross-compiled; native service-update validation is pending.

## Manual Server updates

**Servers → Update** checks the latest stable GitHub Release for the selected
Server's OS/architecture. **Download and restart** queues an explicit update;
there is no scheduled or automatic upgrade. Like Agent updates, the Server must
run under the built-in `graphwan server service` command. Foreground or manually
written services running `graphwan server` do not enable the update button.
Existing built-in installations gain this capability after starting a binary
that includes Server updates.

The selected Server downloads directly from GitHub and uses the shared verified
staging, separate helper service and startup-failure binary rollback mechanism.
Configuration, CA, cluster identity and service arguments are retained. The
panel displays version, platform, phase and failure details. When updating the
Server serving the panel, the connection closes during restart and a new login
may be required. A single-Server deployment has a brief control-plane outage;
existing Agent data-plane traffic continues. With two voting Servers, stopping
one temporarily prevents quorum writes.

Update requests are stored in replicated configuration and executed only by the
named Server, using existing cluster channels regardless of which replica serves
the panel. Only one Server update may be pending/in progress at a time. The
request requires quorum to commit; the updater persists processed IDs so it will
not reinstall after reconnecting or restarting. Download/install failures are
reported and may be retried manually. Native validation is Linux-only; other
supported service platforms are cross-compiled.
