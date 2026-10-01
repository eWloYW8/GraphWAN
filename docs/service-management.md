# Service management

GraphWAN includes system service management for both Agent and Server:

- Linux: systemd, running as root.
- Windows: native Service Control Manager integration, running as LocalSystem.
- macOS: system launchd LaunchDaemon, running as root.

No additional wrapper executable is required. Other platforms retain the foreground
commands. Installation and service changes require root/an elevated administrator
terminal. Windows and macOS builds are supported, but native execution has not
been verified in the current Linux development environment.

## Register, install and start an Agent

Keep the GraphWAN executable in a permanent, administrator-controlled location.
On Windows, place the architecture-matching official `wintun.dll` beside it
(see [Windows setup](windows-operation.md)). Installation records the executable's
resolved absolute path; it does not copy the binary or download drivers.

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

## Logs, shutdown and upgrades

Linux logs are available through `journalctl -u graphwan-agent` (or the Server
service name). macOS writes `/var/log/NAME.out.log` and
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
