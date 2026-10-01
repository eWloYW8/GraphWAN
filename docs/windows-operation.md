# Windows Wintun adapter

The Windows adapter uses Wintun for packets and IP Helper APIs for addresses,
routes and per-family MTUs. Native Windows execution is **not yet verified** in
the current development environment. The production binary supports Windows
amd64, arm64 and 386 build targets.

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
route. Use the built-in [service commands](service-management.md) to install and manage
a Windows service. No external service wrapper is needed.

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

## Automatic interface endpoints

Discovery uses `GetIfTable2Ex` interface metadata. An adapter must report itself
as hardware, must not be a filter or endpoint interface, and must not have a
loopback, virtual, tunnel or bridge interface type. Alias names and MAC-address
presence do not determine eligibility. Ethernet, Wi-Fi and cellular interfaces
can advertise their global-unicast and link-local IPv4/IPv6 addresses when up; each address gets
TCP and UDP endpoints at the configured Agent port. Guest NIC eligibility depends
on the hardware flag reported by its driver. Shared discovery attaches the owner's
IPv6 scope, and Mesh maps it to local dialing interfaces. See
[discovery and live updates](endpoint-resolution.md#automatic-interface-discovery).
