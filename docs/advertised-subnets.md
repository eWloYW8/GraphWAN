# Advertised subnets and gateways

A Node can advertise external IP prefixes **within a particular GraphWAN Network**.
In the network detail page, enter edit mode, select the Node, and add entries under
**Advertised subnets**. Each entry has a prefix and an **Automatic gateway** mode:

| Mode | GraphWAN behavior on the advertising Node |
|---|---|
| Off | Admit and route the prefix inside GraphWAN; leave host forwarding, firewall and NAT configuration to the administrator. |
| Routing | Enable host IP forwarding and install scoped allow rules. Preserve source addresses. |
| SNAT (Linux) | Enable forwarding and scoped allow rules, and masquerade traffic entering the Network's TUN toward this prefix. |

Linux supports all three modes. Windows and macOS support Off and Routing.
SNAT on Windows/macOS reports an Agent configuration error; it never silently
falls back to Routing. Other platforms support Off. Windows/macOS implementations
are cross-compiled but have not been verified on native hosts.

## Routes and return traffic

Advertising a prefix does **not** install operating-system routes to it on other
Nodes. Administrators must direct the desired traffic into the appropriate
GraphWAN TUN interface. The gateway's existing host routes determine how it reaches
the external subnet. GraphWAN continues to manage its own TUN address and connected
overlay subnet route.

For example, Network `10.42.0.0/24` contains A (`10.42.0.1`) and B (`10.42.0.2`).
B advertises `192.168.10.0/24` and has a LAN address of `192.168.10.1`.

On a Linux A, after identifying its GraphWAN interface:

```sh
sudo ip route add 192.168.10.0/24 dev <A-GraphWAN-interface>
```

In Routing mode, LAN devices or their upstream router need a route for
`10.42.0.0/24` via `192.168.10.1`. Adding a route only on B cannot teach LAN
devices where to send their replies. SNAT usually removes that requirement:
external devices see B's egress address, and connection tracking returns replies
to A. Access policies on external devices remain under their administrators'
control.

Source prefixes behind other GraphWAN gateways are supported by the data plane,
but site-to-site operation also needs administrator-managed routes on both sides.
Windows/macOS automatic firewall rules cover traffic between the overlay prefix
and the local advertised subnet; additional site-to-site firewall policy is manual.

GraphWAN chooses the longest matching advertised prefix, then follows the existing
weighted topology to its owner. Down-edge filtering still applies. If the selected
gateway is unreachable, packets drop instead of falling back to a broader prefix.
Both requests and replies must match their declared Node ownership, including on
intermediate hops. This extends the existing hop-authenticated admission model;
it does not introduce end-to-end source authentication across untrusted relays.

## Validation and scope

- Entries use canonical CIDRs of the Network's address family, such as
  `192.168.10.0/24` rather than `192.168.10.7/24`.
- A prefix can occur only once per Network, including entries on different Nodes.
  Different-length overlapping prefixes are permitted.
- A subnet wholly inside the overlay CIDR is rejected. Broader prefixes, including
  `0.0.0.0/0` or `::/0`, are permitted. Registered overlay IPs take precedence;
  unknown addresses inside the overlay never fall through to an external gateway.
- Loopback, multicast and link-local destinations are not carried by external
  prefix matching.
- Limits are 64 entries per Node and 1,024 per Network.

Upgrade the controller and participating Agents before using advertised subnets;
older Agents do not implement prefix admission or gateway configuration.

## Host configuration

Automatic modes require administrative privileges. They do not configure DNS,
default routes, remote LAN routers, or the destination devices' firewalls.
Other firewall engines can still explicitly block forwarded traffic. GraphWAN
keeps their rules and global policies intact; administrators must resolve those
conflicts when configuring a gateway.

**Linux:** install `nft` and use a kernel supporting inet-family NAT. GraphWAN owns
a dedicated `inet graphwan_<agent-id>` table and replaces its rules atomically.
When iptables/ip6tables compatibility tools are installed, it also owns a hashed
`GW...` filter chain and inserts a scoped FORWARD jump, allowing its gateway
traffic through a default DROP policy. It removes only its own jump/chain. Other
nft/firewalld base-chain drops can still reject packets.
Forwarding rules match the TUN interface and advertised prefix. Masquerading
additionally matches the ingress TUN and excludes the overlay destination range.
More-specific Off/Routing entries take precedence over broader SNAT entries.
Global IPv4/IPv6 forwarding is enabled only for families with automatic entries.
Enabling IPv6 forwarding changes the host's router-advertisement behavior; hosts
that obtain their underlay route through RA may need administrator-managed
`accept_ra` settings.

**Windows:** PowerShell enables forwarding on the Wintun interface and the
interfaces selected by existing external routes. Owned Windows Firewall rules
use the group `GraphWAN-Gateway-<agent-id>`. If an external route changes to a
different interface, restart the Agent to prepare that interface. An existing
route must be available when applying the configuration.

**macOS:** system IPv4/IPv6 forwarding is enabled. GraphWAN loads only its
`com.apple/graphwan_<agent-id>` child anchor; when PF is enabled, the standard
`com.apple/*` filter anchor must be present. It does not replace the root PF
configuration or enable a disabled PF. PF policy changes made independently
require reapplying the gateway configuration or restarting the Agent.

Owned rules are reconciled when configuration or a recovered TUN interface changes,
and removed on clean shutdown, network removal, or disabling automatic modes.
Stable Agent ownership allows stale rules to be replaced/removed after a crash.
Failures are reported through the Agent's configuration error; partial updates
attempt to restore the previous owned rules.

Shared forwarding settings stay enabled when removing rules, since other software
may depend on them. Removing SNAT rules does not purge the host's connection
tracking table: existing translated flows can persist until their state expires.
New connections use the new mode.
