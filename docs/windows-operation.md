# Windows Wintun adapter

The Windows adapter uses Wintun for packets and IP Helper APIs for addresses,
routes and per-family MTUs. Native Windows execution is **not yet verified** in
the current development environment. The production binary and native test
binaries cross-build for Windows amd64, arm64 and 386. Linux race tests verify
the shared ring lifetime and rollback algorithms; this does not establish that
Windows kernel networking works. Native and multi-host acceptance remain open in
the [implementation tracker](implementation-status.md).

## Build and install the DLL

Use Go 1.26 or newer. In PowerShell, from the repository:

```powershell
go build -o bin/graphwan.exe ./cmd/graphwan
py -3 scripts/fetch-wintun.py --arch amd64 --output bin
```

Match `--arch` to the **executable** architecture (`amd64`, `arm64` or `386`).
The script requires Python 3 and downloads the official Wintun 0.14.1 archive,
verifies its pinned SHA-256, and copies the selected unchanged `wintun.dll` and
`WINTUN-LICENSE.txt`. It accepts `--archive path/to/wintun-0.14.1.zip` for an
offline copy, with the same checksum check. It bounds the download and extracts
only the two exact archive members. A checksum failure leaves existing files
unchanged. Each output file is replaced atomically, with the license first.

Distribute both files beside `graphwan.exe`. The project does not download a
driver at Agent startup. The DLL loads from the executable directory or Windows
System32, using restricted DLL search flags. The required API exports are checked
before creating an adapter. A missing, incompatible or wrong-architecture DLL
produces a configuration error. The Server role does not need Wintun.

The official [Wintun distribution](https://www.wintun.net/) publishes the signed
archive and checksum. Its prebuilt DLL license is included with the archive;
preserve it when redistributing. The adapter uses the published
[Wintun API](https://git.zx2c4.com/wintun/tree/api/wintun.h) through the MIT-licensed
Go binding and the MIT-licensed WireGuard Windows `winipcfg` package. It does not
modify the signed DLL or call private driver interfaces.

## Run an Agent

Use an elevated administrator PowerShell session and the normal controller
enrollment workflow from the [README](../README.md):

```powershell
$env:GRAPHWAN_ENROLLMENT_TOKEN = '<one-time-token>'
.\bin\graphwan.exe agent --server https://controller.example.com:8443 `
  --ca .\ca.pem --name windows-node --data-dir "$env:LOCALAPPDATA\GraphWAN\agent"
```

The token is needed for initial enrollment. Keep the data directory, identity and
cached configuration under a Windows ACL limited to the operating account and
administrators. Go's Unix permission bits do not themselves enforce Windows ACLs.
Add Network memberships and Edges in the management UI. Permit the configured peer
port (default TCP/UDP 24752) in the host firewall for the intended underlay peers.
The adapter does not change firewall rules, DNS settings or the system default
route. Windows service installation is not implemented by this adapter.

## Ownership and configuration

Each Network creates a new Wintun adapter with an automatically generated name
and GUID. GraphWAN never opens an existing adapter by name. Because Wintun can
rename an existing adapter to satisfy an alias collision, GraphWAN first checks
all interface aliases case-insensitively and holds a cross-process named mutex
through creation. This coordinates GraphWAN processes; another privileged
application creating the same alias does not participate in that mutex. Default
names include 48 random bits.

Address and route operations are restricted to the created adapter's LUID.
Windows permits a prefix on several interfaces, so cleanup selects the owned
LUID instead of deleting routes by prefix globally. Avoid assigning overlay
subnets that overlap underlay routes. Both IPv4 and IPv6 IP MTUs are configured;
the NDIS link MTU reported by some tools is a different value. Router discovery,
automatic metrics and DAD are disabled on the owned IP interfaces. A disabled or
unavailable required IP stack causes configuration to fail.

Live address, prefix and MTU changes preserve the adapter and reader. Failure
restores the old address, subnet route and MTU; a restoration failure marks the
device unavailable so the Agent can recreate it from the applied configuration.
Per-family MTU updates also roll back earlier family changes if a later call
fails. Configuration and Close are serialized.

Each Network uses bounded 1 MiB Wintun rings. Writes are serialized and congestion
drops the affected packet without retiring the device. Close signals a separate
manual-reset event, waits for operations holding mapped packet storage, then ends
the session and closes the adapter. It does not uninstall the shared Wintun driver.

## Pending native verification

Run on a disposable elevated Windows host with both IP stacks enabled. The tests
create temporary adapters and routes; the opt-in flag does not prove isolation.
Build the test binaries into the same directory as the matching DLL:

```powershell
go test -c -tags integration -o bin/tunnel.test.exe ./internal/tunnel
go test -c -tags integration -o bin/agent.test.exe ./internal/agent
$env:GRAPHWAN_TEST_WINDOWS = '1'
$testProgram = (Resolve-Path .\bin\tunnel.test.exe).Path
$ruleName = 'GraphWAN-test-' + [guid]::NewGuid().ToString()
New-NetFirewallRule -Name $ruleName -DisplayName $ruleName -Direction Inbound `
  -Action Allow -Program $testProgram -Protocol UDP -LocalPort 54321 `
  -RemoteAddress '10.240.0.0/16','fd42::/16' -Profile Any -ErrorAction Stop
try {
  & $testProgram -test.run=TestNativeWindows -test.v -test.timeout=180s
  if ($LASTEXITCODE -ne 0) { throw 'Native TUN tests failed' }
  .\bin\agent.test.exe -test.run=TestNativeWindowsConfigurationReconcile -test.v -test.timeout=180s
  if ($LASTEXITCODE -ne 0) { throw 'Native Agent test failed' }
} finally {
  Remove-NetFirewallRule -Name $ruleName
  Remove-Item Env:GRAPHWAN_TEST_WINDOWS
}
```

The test-specific firewall exception permits UDP delivery to the test receiver;
it is removed in `finally` and does not disable the firewall. Existing block rules
or endpoint-security policy may still prevent traffic and require investigation.

Pending kernel tests cover full-MTU IPv4/IPv6 UDP in both directions at 1280 and
9000 bytes, subnet routes, duplicate/case-insensitive names, simultaneous name
creation, blocked-read interruption, idempotent Close and removal. Seven migration
cases check prefix narrowing/widening, host addresses and family switches.
Another case verifies that migrating and closing an adapter preserves a different
adapter's route for the same prefix. The Agent test uses two real Wintun devices,
checks in-place edits against the kernel, injects a second-Network rejection to
verify first-Network rollback, and checks shutdown cleanup. That injected failure
does not verify an actual Windows kernel error path.

Already executed Linux checks cover ring storage lifetime, lost-wakeup shutdown,
concurrent writes/Close, congestion recovery and configuration/route rollback.
The downloader has been checked against all four official DLL architectures,
including PE machine identifiers, unchanged DLL/license bytes and rejection of a
corrupted archive. Native Windows tests above, process-crash cleanup, multi-host
forwarding, endpoint discovery, NAT behavior and service deployment remain pending.


## UDP packet information

Windows UDP listeners enable native `IP_PKTINFO` and `IPV6_PKTINFO` as appropriate
for their socket family. Dual-stack sockets enable both. Setup errors fail
listener creation and close its socket; the documented IPv4-disabled exception is
accepted only after a socket probe confirms that IPv4 is unavailable. Standalone
UDP and the shared UDP/QUIC listener use the same setup.

The first received datagram's packet-info object supplies the local source
address and interface index for replies. The implementation selects only the
matching IPv4 or IPv6 object from `WSACMSGHDR` data, validates lengths and alignment,
and copies it out of the reusable receive buffer. IPv4-mapped peers use IPv4
packet information. Truncated payload/control data and `WSAEMSGSIZE` discard one
packet while keeping the listener alive.

The implementation follows Microsoft's [dual-stack socket requirements](https://learn.microsoft.com/en-us/windows/win32/winsock/dual-stack-sockets),
[control-header layout](https://learn.microsoft.com/en-us/windows/win32/api/ws2def/ns-ws2def-wsacmsghdr)
and [send-message source-address rules](https://learn.microsoft.com/en-us/windows/win32/api/winsock2/nf-winsock2-wsasendmsg).
It uses Go's overlapped `ReadMsgUDPAddrPort`/`WriteMsgUDPAddrPort` operations and
`x/sys/windows` socket options; it does not create a second data socket for STUN
or reply traffic.

Portable Linux tests verify independent 32/64-bit ABI byte fixtures, control
chains/padding, interface-index preservation, mapped peers, owned output storage,
invalid/truncated lengths, duplicate packet-info objects and invalid source
addresses. A bounded fuzz run completed 81,043 executions without failure.
Linux real sockets verify oversized-packet rejection followed by successful
IPv4/IPv6 replies, with and without a shared QUIC listener. These checks do not
prove Windows kernel behavior.

On a Windows host with IPv4 and IPv6 enabled, the following native checks do not
need Wintun or administrator rights:

```powershell
go test ./internal/transport -run 'TestWinsock|TestUDPWildcardReplySource' -count=10
```

The tests compare the Go Windows ABI structures, exercise the socket options,
send to a secondary IPv4 loopback address and IPv6 loopback, inject oversized UDP
packets and require replies from the intended address. Native execution remains
pending, along with multi-interface/NAT acceptance.

## Automatic interface endpoints

Discovery uses `GetIfTable2Ex` interface metadata. An adapter must report itself
as hardware, must not be a filter or endpoint interface, and must not have a
loopback, virtual, tunnel or bridge interface type. Alias names and MAC-address
presence do not determine eligibility. Ethernet, Wi-Fi and cellular interfaces
can advertise their global-unicast IPv4/IPv6 addresses when up; each address gets
TCP and UDP endpoints at the configured Agent port. Guest NIC eligibility depends
on the hardware flag reported by its driver. IPv6 link-local discovery is pending.

Linux fixture tests cover the metadata decision, alias-independent classification,
duplicate addresses, deterministic IDs and rejecting partial address snapshots.
Production and discovery tests cross-compile for Windows amd64, arm64 and 386.
Actual Windows enumeration and driver flags remain unverified. Native acceptance
must compare advertised endpoints with Ethernet/Wi-Fi/cellular and guest NICs,
confirm Wintun/TAP/bridge exclusion after alias changes, and exercise address
addition/removal and down/up transitions while another Link keeps carrying data.
See [discovery and live updates](endpoint-resolution.md#automatic-interface-discovery).
