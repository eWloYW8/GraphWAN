#!/usr/bin/env python3
"""Run only inside a disposable root network namespace; never modifies host routes."""
import argparse
import contextlib
import http.cookiejar
import hashlib
import json
import os
from pathlib import Path
import socket
import ssl
import subprocess
import tempfile
import time
import urllib.error
import urllib.parse
import urllib.request
import uuid


def command(*args, check=True, **kwargs):
    return subprocess.run(args, check=check, capture_output=True, text=True, **kwargs)


def eventually(fn, timeout=30):
    deadline = time.monotonic() + timeout
    last = None
    while time.monotonic() < deadline:
        try:
            result = fn()
            if result:
                return result
        except (OSError, urllib.error.URLError, AssertionError) as error:
            last = error
        time.sleep(0.15)
    raise AssertionError(f"condition timed out: {last}")


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--binary", required=True)
    parser.add_argument("--server-transports", nargs=3, choices=("tcp", "websocket", "grpc", "wss"), default=["tcp"] * 3,
                        help="controller carrier for each of the three agents")
    parser.add_argument("--transport", choices=("auto", "udp", "tcp", "ws", "wss", "grpc", "quic"), default="auto",
                        help="auto/udp/tcp use discovered endpoints; other transports require manual endpoints")
    parser.add_argument("--cipher", choices=("aes-128-gcm", "aes-256-gcm", "chacha20-poly1305", "xchacha20-poly1305"), default="chacha20-poly1305", help="network authenticated encryption suite")
    parser.add_argument("--mtu", type=int, default=1280, help="overlay TUN MTU")
    parser.add_argument("--underlay-mtu", type=int, default=1500, help="isolated bridge/veth MTU")
    parser.add_argument("--overlay-family", type=int, choices=(4, 6), default=4, help="virtual network address family")
    parser.add_argument("--underlay-family", type=int, choices=(4, 6), default=4, help="controller and peer underlay address family")
    parser.add_argument("--nat", action="store_true", help="place each agent behind a separate restricted NAT; requires --transport udp/tcp and iptables")
    parser.add_argument("--restricted-agent", action="store_true", help="run agents with the capability bounding set used by the systemd example")
    parser.add_argument("--link-local", action="store_true", help="use only IPv4 link-local underlay addresses on the shared link")
    parser.add_argument("--peer-link-local-v6", action="store_true", help="IPv6 link-local-only peer data with distinct NIC names; IPv4 controller")
    parser.add_argument("--punch-only", action="store_true", help="disable both direct methods and exercise TCP/UDP source-port punching without requiring NAT")
    parser.add_argument("--nat-nodes", type=int, choices=(1, 2, 3), default=3, help="with --nat, place the first N agents behind separate NATs; leave the rest public")
    parser.add_argument("--nat-remap", action="store_true", help="replace live NAT mappings and require automatic rediscovery/reconnection")
    parser.add_argument("--conntrack", default="/usr/sbin/conntrack", help="conntrack executable used only by --nat-remap")
    parser.add_argument("--listen-port-change", action="store_true", help="verify failed-bind rollback and live listener replacement without Agent restart")
    args = parser.parse_args()
    if args.listen_port_change and (args.nat or args.punch_only or args.peer_link_local_v6):
        parser.error("--listen-port-change uses unscoped direct endpoints without NAT")
    if args.nat_remap and not args.nat:
        parser.error("--nat-remap requires --nat")
    if args.nat_remap and not Path(args.conntrack).is_file():
        parser.error("--nat-remap requires conntrack (or --conntrack /path/to/conntrack)")
    if args.nat_nodes != 3 and not args.nat:
        parser.error("--nat-nodes requires --nat")
    if args.punch_only and args.transport not in ("auto", "udp", "tcp"):
        parser.error("--punch-only supports automatic TCP/UDP candidates")
    if args.nat and args.transport not in ("udp", "tcp"):
        parser.error("--nat verifies UDP or TCP punching")
    if args.nat and args.underlay_family != 4:
        parser.error("the NAT fixture currently models IPv4 SNAT; IPv6 overlay is supported")
    if args.link_local and (args.nat or args.underlay_family != 4):
        parser.error("--link-local requires IPv4 underlay without NAT")
    if args.peer_link_local_v6 and (args.nat or args.underlay_family != 4 or args.link_local):
        parser.error("--peer-link-local-v6 requires IPv4 controller underlay without NAT or --link-local")
    if not 1280 <= args.mtu <= 9000 or not 1280 <= args.underlay_mtu <= 9000:
        parser.error("MTUs must be between 1280 and 9000")
    if os.geteuid() != 0 or os.readlink("/proc/self/ns/net") == os.readlink("/proc/1/ns/net"):
        raise SystemExit("Run with sudo unshare --net; an isolated network namespace is required")
    controller_ip = "192.0.2.1" if args.underlay_family == 4 else "2001:db8:42::1"
    underlay_ips = [f"192.0.2.{11+i}" if args.underlay_family == 4 else f"2001:db8:42::{11+i}" for i in range(3)]
    nat_ports = {kind: [base+i for i in range(3)] for kind, base in (("udp", 32752), ("tcp", 33752))}
    underlay_bits = 24 if args.underlay_family == 4 else 64
    if args.link_local:
        controller_ip = "169.254.42.1"
        underlay_ips = [f"169.254.42.{11+i}" for i in range(3)]
        underlay_bits = 16
    interface_names = [f"uplink{i}" if args.peer_link_local_v6 else "eth0" for i in range(3)]
    peer_ips = [f"fe80::42:{i+1}%25{interface_names[i]}" for i in range(3)] if args.peer_link_local_v6 else underlay_ips
    peer_family = 6 if args.peer_link_local_v6 else args.underlay_family
    overlay_ips = [f"10.42.0.{i+1}" if args.overlay_family == 4 else f"fd42:6777::{i+1}" for i in range(3)]
    overlay_cidr = "10.42.0.0/24" if args.overlay_family == 4 else "fd42:6777::/64"
    def host_port(ip, port):
        return f"[{ip}]:{port}" if ":" in ip else f"{ip}:{port}"
    server_url = "https://" + host_port(controller_ip, 8443)
    address_flags = ["nodad"] if args.underlay_family == 6 else []
    binary = str(Path(args.binary).resolve())
    processes = []
    logs = []
    with tempfile.TemporaryDirectory(prefix="graphwan-e2e-") as root:
        root = Path(root)

        def spawn(name, argv, env=None):
            path = root / f"{name}.log"
            stream = path.open("w")
            logs.append((path, stream))
            process = subprocess.Popen(argv, stdout=stream, stderr=stream, env=env)
            processes.append(process)
            return process

        def stop(process):
            if process.poll() is None:
                process.terminate()
                try:
                    process.wait(timeout=15)
                except subprocess.TimeoutExpired:
                    process.kill()
                    process.wait(timeout=5)
            if process.returncode not in (0, -15):
                raise AssertionError(f"process exited with status {process.returncode}")

        try:
            command("ip", "link", "set", "lo", "up")
            command("ip", "link", "add", "gw-underlay", "type", "bridge")
            command("ip", "link", "set", "gw-underlay", "mtu", str(args.underlay_mtu))
            command("ip", "addr", "add", f"{controller_ip}/{underlay_bits}", "dev", "gw-underlay", *address_flags)
            command("ip", "link", "set", "gw-underlay", "up")
            if args.peer_link_local_v6:
                command("ip", "link", "add", "gw-link-local", "type", "bridge")
                command("ip", "link", "set", "gw-link-local", "mtu", str(args.underlay_mtu))
                command("ip", "link", "set", "gw-link-local", "up")
            namespaces = []
            routers = []
            for index in range(3):
                holder = spawn(f"namespace-{index}", ["unshare", "--net", "sleep", "900"])
                eventually(lambda: os.readlink(f"/proc/{holder.pid}/ns/net") != os.readlink("/proc/self/ns/net"))
                namespaces.append(holder)
                gateway = holder
                if args.nat and index < args.nat_nodes:
                    gateway = spawn(f"nat-{index}", ["unshare", "--net", "sleep", "900"])
                    eventually(lambda: os.readlink(f"/proc/{gateway.pid}/ns/net") != os.readlink("/proc/self/ns/net"))
                    routers.append(gateway)
                outside, inside = f"gwh{index}", f"gwn{index}"
                command("ip", "link", "add", outside, "type", "veth", "peer", "name", inside)
                command("ip", "link", "set", outside, "mtu", str(args.underlay_mtu))
                command("ip", "link", "set", inside, "mtu", str(args.underlay_mtu))
                command("ip", "link", "set", outside, "master", "gw-underlay")
                command("ip", "link", "set", outside, "up")
                command("ip", "link", "set", inside, "netns", str(gateway.pid))
                prefix = ["nsenter", "-t", str(gateway.pid), "-n"]
                command(*prefix, "ip", "link", "set", "lo", "up")
                nic = interface_names[index]
                command(*prefix, "ip", "link", "set", inside, "name", nic)
                command(*prefix, "ip", "link", "set", nic, "addrgenmode", "none")
                command(*prefix, "ip", "addr", "add", f"{underlay_ips[index]}/{underlay_bits}", "dev", nic, *address_flags)
                if args.peer_link_local_v6:
                    command(*prefix, "ip", "addr", "add", f"fe80::42:{index+1}/64", "dev", nic, "nodad")
                command(*prefix, "ip", "link", "set", nic, "up")
                if args.peer_link_local_v6:
                    # The same link-local address exists on two independent links.
                    # Owner and initiator zones must both be honored, never guessed.
                    command("ip", "link", "add", f"gws{index}", "type", "veth", "peer", "name", f"backup{index}")
                    command("ip", "link", "set", f"gws{index}", "mtu", str(args.underlay_mtu))
                    command("ip", "link", "set", f"gws{index}", "master", "gw-link-local")
                    command("ip", "link", "set", f"gws{index}", "up")
                    command("ip", "link", "set", f"backup{index}", "netns", str(holder.pid))
                    command(*prefix, "ip", "link", "set", f"backup{index}", "mtu", str(args.underlay_mtu), "addrgenmode", "none")
                    command(*prefix, "ip", "addr", "add", f"fe80::42:{index+1}/64", "dev", f"backup{index}", "nodad")
                    command(*prefix, "ip", "link", "set", f"backup{index}", "up")
                if args.peer_link_local_v6 and args.punch_only:
                    # Hole punch is independent of the direct-family switches and
                    # therefore also tries the IPv4 management endpoints. Block
                    # their peer port in this fixture to prove IPv6-only data.
                    for protocol in ("tcp", "udp"):
                        command(*prefix, "/usr/sbin/iptables", "-A", "OUTPUT", "-p", protocol, "--dport", "24752", "-j", "REJECT")
                if args.nat and index < args.nat_nodes:
                    command("ip", "link", "add", f"lan{index}", "type", "veth", "peer", "name", f"client{index}")
                    command("ip", "link", "set", f"lan{index}", "netns", str(gateway.pid))
                    command("ip", "link", "set", f"client{index}", "netns", str(holder.pid))
                    command(*prefix, "ip", "link", "set", f"lan{index}", "name", "lan")
                    command(*prefix, "ip", "addr", "add", f"10.200.{index}.1/24", "dev", "lan")
                    command(*prefix, "ip", "link", "set", "lan", "up")
                    command(*prefix, "ip", "route", "add", "default", "via", "192.0.2.1")
                    command(*prefix, "/usr/sbin/sysctl", "-qw", "net.ipv4.ip_forward=1")
                    iptables = [*prefix, "/usr/sbin/iptables"]
                    # Unsolicited datagrams must be dropped before conntrack
                    # confirmation, rather than becoming connections to the router.
                    command(*iptables, "-A", "INPUT", "-i", "eth0", "-p", "udp", "-j", "DROP")
                    command(*iptables, "-A", "INPUT", "-i", "eth0", "-p", "tcp", "-j", "DROP")
                    command(*iptables, "-A", "FORWARD", "-i", "lan", "-o", "eth0", "-p", "tcp", "--sport", "24752", "-m", "multiport", "--dports", "24752,33752:33754", "--tcp-flags", "SYN,ACK", "SYN", "-m", "comment", "--comment", "graphwan-syn", "-j", "ACCEPT")
                    command(*iptables, "-A", "FORWARD", "-i", "lan", "-o", "eth0", "-j", "ACCEPT")
                    command(*iptables, "-A", "FORWARD", "-m", "conntrack", "--ctstate", "ESTABLISHED,RELATED", "-j", "ACCEPT")
                    command(*iptables, "-A", "FORWARD", "-j", "DROP")
                    # Fixed remapping proves that STUN must publish the mapped port,
                    # while conntrack admits replies only from contacted peers.
                    command(*iptables, "-t", "nat", "-A", "POSTROUTING", "-o", "eth0", "-p", "udp", "--sport", "24752", "-j", "SNAT", "--to-source", f"192.0.2.{11+index}:{nat_ports['udp'][index]}")
                    command(*iptables, "-t", "nat", "-A", "POSTROUTING", "-o", "eth0", "-p", "tcp", "--sport", "24752", "-j", "SNAT", "--to-source", f"192.0.2.{11+index}:{nat_ports['tcp'][index]}")
                    command(*iptables, "-t", "nat", "-A", "POSTROUTING", "-o", "eth0", "-j", "MASQUERADE")
                    client_prefix = ["nsenter", "-t", str(holder.pid), "-n"]
                    command(*client_prefix, "ip", "link", "set", "lo", "up")
                    command(*client_prefix, "ip", "link", "set", f"client{index}", "name", "eth0")
                    command(*client_prefix, "ip", "addr", "add", f"10.200.{index}.2/24", "dev", "eth0")
                    command(*client_prefix, "ip", "link", "set", "eth0", "up")
                    command(*client_prefix, "ip", "route", "add", "default", "via", f"10.200.{index}.1")

            if args.nat:
                stun = spawn("stun", ["python3", "-u", str(Path(__file__).with_name("stun_server.py"))])
                eventually(lambda: "ready" in (root / "stun.log").read_text())

            password = uuid.uuid4().hex
            server = spawn("controller", [binary, "server", "--listen", host_port(controller_ip, 8443), "--tls-hosts", controller_ip, "--data-dir", str(root / "server")], dict(os.environ, GRAPHWAN_ADMIN_PASSWORD=password))
            ca = root / "server" / "ca.pem"
            eventually(lambda: ca.exists() and server.poll() is None)
            context = ssl.create_default_context(cafile=str(ca))
            opener = urllib.request.build_opener(urllib.request.HTTPSHandler(context=context), urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()), urllib.request.ProxyHandler({}))
            csrf = ""

            def api(method, path, body=None, revision=None):
                raw = None if body is None else json.dumps(body).encode()
                headers = {"Content-Type": "application/json", "X-CSRF-Token": csrf}
                if revision is not None:
                    headers["If-Match"] = str(revision)
                request = urllib.request.Request(server_url + "/api/v1" + path, data=raw, headers=headers, method=method)
                with opener.open(request, timeout=5) as response:
                    return json.load(response)

            eventually(lambda: api("GET", "/health")["status"] == "ok")
            csrf = api("POST", "/login", {"password": password})["csrf_token"]
            agents, agent_commands = [], []
            for index, holder in enumerate(namespaces):
                token = api("POST", "/enrollment-tokens", {})["token"]
                argv = ["nsenter", "-t", str(holder.pid), "-n"]
                if args.restricted_agent:
                    argv += ["setpriv", "--bounding-set=-all,+net_admin,+net_bind_service", "--no-new-privs"]
                argv += [binary, "agent", "--server", server_url, "--server-transport", args.server_transports[index], "--ca", str(ca), "--name", f"node-{index}", "--data-dir", str(root / f"agent-{index}")]
                agent_commands.append(argv)
                agents.append(spawn(f"agent-{index}", argv, dict(os.environ, GRAPHWAN_ENROLLMENT_TOKEN=token, HTTP_PROXY="", HTTPS_PROXY="", ALL_PROXY="")))
            state = eventually(lambda: (s if len(s["agents"]) == 3 and all(a["endpoints"] for a in s["agents"]) else None) if (s := api("GET", "/state")) else None)
            if args.link_local or args.peer_link_local_v6:
                for index, item in enumerate(sorted(state["agents"], key=lambda item: item["name"])):
                    actual = {(endpoint["transport"], endpoint["url"]) for endpoint in item["endpoints"]}
                    expected = {(kind, f"{kind}://{underlay_ips[index]}:24752") for kind in ("udp", "tcp")}
                    if args.peer_link_local_v6:
                        expected |= {(kind, f"{kind}://{host_port(ip, 24752)}") for kind in ("udp", "tcp") for ip in (peer_ips[index], f"fe80::42:{index+1}%25backup{index}")}
                    assert actual == expected, f"unexpected link-local discovery: {actual}"
                print("PASS: automatic TCP/UDP link-local endpoints match each node’s interface scope", flush=True)
            if args.restricted_agent:
                for process in agents:
                    status = dict(line.split(":", 1) for line in Path(f"/proc/{process.pid}/status").read_text().splitlines())
                    for field in ("CapBnd", "CapEff"):
                        assert int(status[field], 16) == ((1 << 12) | (1 << 10)), f"unexpected Agent {field}"
                    assert status["NoNewPrivs"].strip() == "1", "Agent can gain new privileges"
                print("PASS: agents have only NET_ADMIN/NET_BIND_SERVICE and cannot gain privileges", flush=True)
            by_name = {a["name"]: a for a in state["agents"]}
            if args.nat:
                stun_services = ["192.0.2.1:3478"]
                if args.transport == "tcp":
                    stun_services.append("tcp://192.0.2.1:3478")
                for agent in by_name.values():
                    def configure_stun():
                        try:
                            return api("PATCH", f"/agents/{agent['id']}", {"stun_servers": stun_services}, api("GET", "/state")["revision"])
                        except urllib.error.HTTPError as error:
                            if error.code == 409:
                                return None
                            raise
                    eventually(configure_stun)
                def observed():
                    state = api("GET", "/state")
                    for agent in state["agents"]:
                        index = int(agent["name"].split("-")[1])
                        if index >= args.nat_nodes:
                            # A public address already advertised by its NIC takes
                            # precedence over the identical STUN observation.
                            for protocol in ("udp", "tcp"):
                                if not any(e["source"] == "interface" and e["url"] == f"{protocol}://192.0.2.{11+index}:24752" for e in agent["endpoints"]):
                                    return False
                            continue
                        expected = [f"udp://192.0.2.{11+index}:{nat_ports['udp'][index]}"]
                        if args.transport == "tcp":
                            expected.append(f"tcp://192.0.2.{11+index}:{nat_ports['tcp'][index]}")
                        for url in expected:
                            if not any(e["source"] == "observed" and e["url"] == url for e in agent["endpoints"]):
                                return False
                    return True
                eventually(observed)
                print(f"PASS: STUN discovered independently translated ports through {args.nat_nodes} NATs; {3-args.nat_nodes} peers use public interface endpoints", flush=True)
                with socket.socket(socket.AF_INET, socket.SOCK_DGRAM) as probe:
                    probe.bind(("192.0.2.1", 0))
                    for index in range(args.nat_nodes):
                        probe.sendto(b"unsolicited", (f"192.0.2.{11+index}", nat_ports["udp"][index]))
                if args.transport == "tcp":
                    for index in range(args.nat_nodes):
                        with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as probe:
                            probe.settimeout(0.1)
                            assert probe.connect_ex((f"192.0.2.{11+index}", nat_ports["tcp"][index])) != 0, "NAT allowed unsolicited TCP"
                def restricted():
                    for router in routers:
                        rules = command("nsenter", "-t", str(router.pid), "-n", "/usr/sbin/iptables", "-L", "INPUT", "-nvx").stdout
                        if not any(len(fields := row.split()) > 3 and fields[2] == "DROP" and fields[3] == args.transport and int(fields[0]) > 0 for row in rules.splitlines()):
                            return False
                    return True
                eventually(restricted)
                print(f"PASS: all {args.nat_nodes} NATs reject unsolicited incoming {args.transport.upper()} before peer punching", flush=True)
            transports = ["udp", "tcp"] if args.transport == "auto" else [args.transport]
            if args.transport not in ("auto", "udp", "tcp"):
                for index in range(3):
                    agent = by_name[f"node-{index}"]
                    path = "" if args.transport in ("udp", "tcp", "quic") else "/custom/overlay"
                    endpoint = {"id": uuid.uuid4().hex, "source": "manual", "transport": args.transport,
                                "url": f"{args.transport}://{host_port(peer_ips[index], 24752)}{path}"}
                    manual_endpoints = [endpoint]
                    if args.peer_link_local_v6:
                        manual_endpoints.append({**endpoint, "id": uuid.uuid4().hex, "url": endpoint["url"].replace(f"%25uplink{index}", f"%25backup{index}")})
                    def configure_endpoint():
                        try:
                            return api("PATCH", f"/agents/{agent['id']}", {"manual_endpoints": manual_endpoints}, api("GET", "/state")["revision"])
                        except urllib.error.HTTPError as error:
                            if error.code == 409:
                                return None
                            raise
                    eventually(configure_endpoint)
            nodes = [{"id": uuid.uuid4().hex, "agent_id": by_name[f"node-{i}"]["id"], "name": f"node-{i}", "address": overlay_ips[i]} for i in range(3)]
            network = {"name": "native three-node test", "cipher": args.cipher, "cidr": overlay_cidr, "nodes": nodes, "edges": [{"id": uuid.uuid4().hex, "a": nodes[i]["id"], "b": nodes[i+1]["id"], "weight": 10, "enabled": True, "transports": transports, "methods": {"ipv4_direct": peer_family == 4, "ipv6_direct": peer_family == 6, "hole_punch": False}} for i in range(2)]}
            network["mtu"] = args.mtu
            if args.nat or args.punch_only:
                for edge in network["edges"]:
                    edge["methods"] = {"ipv4_direct": False, "ipv6_direct": False, "hole_punch": True}
            def create_network():
                try:
                    return api("POST", "/networks", network, api("GET", "/state")["revision"])
                except urllib.error.HTTPError as error:
                    if error.code == 409:
                        return None
                    raise
            eventually(create_network)
            expected_punch = {}
            expected_scoped = {}
            backup_scoped = set()
            if args.peer_link_local_v6:
                current = api("GET", "/state")
                endpoints = {a["id"]: a["endpoints"] for a in current["agents"]}
                def digest(raw):
                    return hashlib.sha256(raw.encode()).hexdigest()[:32]
                for edge in network["edges"]:
                    expected = set()
                    pair = [node for node in nodes if node["id"] in (edge["a"], edge["b"])]
                    for initiator, recipient in (pair, pair[::-1]):
                        for scope in endpoints[initiator["agent_id"]]:
                            if scope["source"] != "interface" or scope["transport"] != "udp" or "%25" not in scope["url"]:
                                continue
                            for target in endpoints[recipient["agent_id"]]:
                                if target["transport"] not in transports or "%25" not in target["url"]:
                                    continue
                                # Only matching links can connect; same IPv6 literals
                                # on the other recipient NIC must fail admission.
                                if ("%25backup" in scope["url"]) != ("%25backup" in target["url"]):
                                    continue
                                method = "punch" if args.punch_only else "direct"
                                base = digest(f"{edge['id']}/{initiator['id']}/{target['id']}/6/{method}")
                                scope_id = digest("/".join(scope[key] for key in ("id", "source", "transport", "url")))
                                candidate_id = digest(f"{base}/scope/{scope_id}")
                                expected.add(candidate_id)
                                if "%25backup" in scope["url"]:
                                    backup_scoped.add(candidate_id)
                    assert len(expected) == 4 * len(transports), "fixture omitted a link or dialing direction"
                    expected_scoped[edge["id"]] = expected
            def punch_candidates():
                result = {}
                current = api("GET", "/state")
                endpoints = {a["id"]: a["endpoints"] for a in current["agents"]}
                for edge in network["edges"]:
                    expected = set()
                    pair = [node for node in nodes if node["id"] in (edge["a"], edge["b"])]
                    for initiator, recipient in (pair, pair[::-1]):
                        index = int(recipient["name"].split("-")[1])
                        for kind in transports:
                            port = 24752
                            if args.nat and index < args.nat_nodes:
                                port = nat_ports[kind][index]
                            url = f"{kind}://{host_port(underlay_ips[index], port)}"
                            endpoint = next(item for item in endpoints[recipient["agent_id"]] if item["url"] == url)
                            identity = f"{edge['id']}/{initiator['id']}/{endpoint['id']}/{peer_family}/punch"
                            expected.add(hashlib.sha256(identity.encode()).hexdigest()[:32])
                    assert len(expected) == 2 * len(transports), "fixture omitted a punch direction"
                    result[edge["id"]] = expected
                return result
            if (args.punch_only or args.nat) and not args.peer_link_local_v6:
                expected_punch = punch_candidates()
            def connected():
                reports = api("GET", "/telemetry")
                if len(reports) != 3:
                    return False
                active = {}
                for report in reports:
                    resources = report.get("resources", {})
                    if resources.get("cpu_percent") is None or resources.get("logical_cpus", 0) < 1 or resources.get("go_memory_bytes", 0) <= 0 or resources.get("goroutines", 0) <= 0:
                        return False
                    assert resources["cpu_percent"] >= 0 and resources["heap_bytes"] <= resources["go_memory_bytes"], "invalid process resource report"
                    healthy = {}
                    candidates = {}
                    for link in report["links"] or []:
                        if link["healthy"]:
                            healthy.setdefault(link["edge_id"], set()).add(link["transport"])
                            candidates.setdefault(link["edge_id"], set()).add(link["candidate_id"])
                        if link["active"]:
                            active.setdefault(link["edge_id"], []).append(link["link_id"])
                    expected = expected_scoped or expected_punch
                    if expected and any(actual != expected[edge_id] for edge_id, actual in candidates.items()):
                        return False
                    if not healthy or any(actual != set(transports) for actual in healthy.values()):
                        return False
                return all(len(active.get(edge["id"], [])) == 2 and len(set(active[edge["id"]])) == 1 for edge in network["edges"])
            eventually(connected)
            if expected_punch:
                print("PASS: both dialing directions have the exact punch-only candidate IDs on every edge", flush=True)
            if expected_scoped:
                print("PASS: every direction/transport on both IPv6 link-local scopes is healthy; mismatched scopes are rejected", flush=True)
                def primary_sessions():
                    return {link["candidate_id"]: link["link_id"] for report in api("GET", "/telemetry") for link in report["links"] or [] if link["healthy"] and link["candidate_id"] not in backup_scoped}
                original_sessions = primary_sessions()
                complete_scoped = expected_scoped
                expected_scoped = {edge_id: candidates - backup_scoped for edge_id, candidates in complete_scoped.items()}
                for index, holder in enumerate(namespaces):
                    command("nsenter", "-t", str(holder.pid), "-n", "ip", "addr", "del", f"fe80::42:{index+1}/64", "dev", f"backup{index}")
                eventually(connected)
                assert primary_sessions() == original_sessions, "scope withdrawal replaced an unaffected primary session"
                for index, holder in enumerate(namespaces):
                    command("nsenter", "-t", str(holder.pid), "-n", "ip", "addr", "add", f"fe80::42:{index+1}/64", "dev", f"backup{index}", "nodad")
                expected_scoped = complete_scoped
                eventually(connected)
                assert primary_sessions() == original_sessions, "scope restoration replaced an unaffected primary session"
                print("PASS: scope withdrawal/restoration revokes and restores only the affected links", flush=True)
            if args.nat and args.transport == "tcp":
                for index, router in enumerate(routers):
                    rules = command("nsenter", "-t", str(router.pid), "-n", "/usr/sbin/iptables", "-L", "FORWARD", "-nvx").stdout
                    syns = sum(int(row.split()[0]) for row in rules.splitlines() if "graphwan-syn" in row)
                    assert syns >= (2 if index == 1 else 1), "TCP NAT opened without outgoing peer SYNs"
                print("PASS: outbound peer SYNs from both sides of each restricted TCP NAT pair", flush=True)
            def ping():
                return command("nsenter", "-t", str(namespaces[0].pid), "-n", "ping", f"-{args.overlay_family}", "-c", "1", "-W", "1", "-M", "do", "-s", str(args.mtu - (28 if args.overlay_family == 4 else 48)), overlay_ips[2], check=False).returncode == 0
            eventually(ping)

            echo_code = f"import socket\ns=socket.socket(socket.AF_INET{'6' if args.overlay_family == 6 else ''});s.setsockopt(socket.SOL_SOCKET,socket.SO_REUSEADDR,1);s.bind(('{overlay_ips[2]}',9876));s.listen()\nwhile True:\n c,_=s.accept()\n with c:\n  while data:=c.recv(65536): c.sendall(data)\n"
            spawn("echo", ["nsenter", "-t", str(namespaces[2].pid), "-n", "python3", "-u", "-c", echo_code])
            exchange_code = f"import socket\ndata=b'graphwan-end-to-end'*8192\ns=socket.create_connection(('{overlay_ips[2]}',9876),3);s.settimeout(10);s.sendall(data);out=b''\nwhile len(out)<len(data):\n chunk=s.recv(65536)\n if not chunk: raise RuntimeError('early EOF')\n out+=chunk\nassert out==data\ns.close()\n"
            def exchange():
                result = command("nsenter", "-t", str(namespaces[0].pid), "-n", "python3", "-c", exchange_code, check=False, timeout=15)
                return result.returncode == 0
            eventually(exchange)
            print(f"PASS: three native TUN agents, {transports} links, IPv{args.overlay_family} overlay/IPv{peer_family} peer underlay, MTU {args.mtu}/{args.underlay_mtu}, multi-hop ICMP and TCP", flush=True)
            if args.listen_port_change:
                transit = by_name["node-1"]["id"]
                prefix = ["nsenter", "-t", str(namespaces[1].pid), "-n"]
                new_port = 25752
                original_pids = [process.pid for process in agents]
                def report():
                    return next(item for item in api("GET", "/telemetry") if item["agent_id"] == transit)
                def owned_tuns():
                    return {(item["ifindex"], item["ifname"], item["mtu"]) for item in json.loads(command(*prefix, "ip", "-j", "link", "show").stdout) if item["ifname"].startswith("gw")}
                original_tuns = owned_tuns()
                assert len(original_tuns) == 1, "expected one transit TUN"
                revision = api("GET", "/state")["revision"]
                eventually(lambda: report()["applied_revision"] >= revision)
                original = report()
                original_links = {link["link_id"] for link in original["links"] or [] if link["healthy"]}
                assert original_links, "missing original transit links"
                ready = root / "port-blocker.ready"
                blocker_code = "import socket,sys,time;from pathlib import Path;s=socket.socket(socket.AF_INET,socket.SOCK_DGRAM);s.bind(('0.0.0.0',int(sys.argv[1])));Path(sys.argv[2]).write_text('ready');time.sleep(300)"
                blocker = spawn("port-blocker", [*prefix, "python3", "-c", blocker_code, str(new_port), str(ready)])
                eventually(lambda: ready.exists() and blocker.poll() is None)
                def patch_transit(body):
                    def attempt():
                        try:
                            return api("PATCH", f"/agents/{transit}", body, api("GET", "/state")["revision"])
                        except urllib.error.HTTPError as error:
                            if error.code == 409:
                                return None
                            raise
                    return eventually(attempt)
                changed = patch_transit({"listen_port": new_port})
                failed = eventually(lambda: current if (current := report()).get("config_error") else None)
                assert "listen for peers" in failed["config_error"], failed["config_error"]
                assert failed["applied_revision"] == original["applied_revision"], "failed bind advanced the applied revision"
                # The controller omits Links from reports behind the desired
                # revision. Adjacent Agents have applied that revision and can
                # prove the original sessions remain healthy on the wire.
                eventually(lambda: original_links <= {link["link_id"] for peer in api("GET", "/telemetry") if peer["agent_id"] != transit for link in peer["links"] or [] if link["healthy"]})
                assert owned_tuns() == original_tuns, "failed bind replaced the TUN"
                eventually(ping)
                eventually(exchange)
                # Listen on both families to prove a failed UDP bind released
                # every partially prepared TCP listener. SO_REUSEADDR permits
                # TIME_WAIT cleanup but does not share an existing listener.
                probe_code = """import socket,sys
sockets=[]
for kind in sys.argv[2:]:
 for family,host in ((socket.AF_INET6,'::'),(socket.AF_INET,'0.0.0.0')):
  s=socket.socket(family,socket.SOCK_STREAM if kind=='tcp' else socket.SOCK_DGRAM)
  if family==socket.AF_INET6: s.setsockopt(socket.IPPROTO_IPV6,socket.IPV6_V6ONLY,1)
  if kind=='tcp': s.setsockopt(socket.SOL_SOCKET,socket.SO_REUSEADDR,1)
  s.bind((host,int(sys.argv[1])))
  if kind=='tcp': s.listen()
  sockets.append(s)
"""
                def port_free(port, *kinds):
                    return command(*prefix, "python3", "-c", probe_code, str(port), *kinds, check=False).returncode == 0
                eventually(lambda: port_free(new_port, "tcp"))
                print("PASS: occupied UDP port rejects reconfiguration, preserves applied revision/TUN/Links/traffic and releases partial TCP binds", flush=True)
                stop(blocker)
                eventually(lambda: not (current := report()).get("config_error") and current["applied_revision"] >= changed["revision"])
                # Use the current state: manual endpoints were added after enrollment.
                current_agent = next(item for item in api("GET", "/state")["agents"] if item["id"] == transit)
                manual = [dict(endpoint) for endpoint in current_agent["endpoints"] if endpoint["source"] == "manual"]
                for endpoint in manual:
                    endpoint["url"] = endpoint["url"].replace(":24752", f":{new_port}")
                if manual:
                    patch_transit({"manual_endpoints": manual})
                def endpoints_updated():
                    current = api("GET", "/state")
                    target = next(item for item in current["agents"] if item["id"] == transit)
                    automatic = [item for item in target["endpoints"] if item["source"] == "interface"]
                    return automatic and all(urllib.parse.urlsplit(item["url"]).port == new_port for item in target["endpoints"]) and report()["applied_revision"] >= current["revision"]
                eventually(endpoints_updated)
                current_agents = {item["id"]: item for item in api("GET", "/state")["agents"]}
                by_node = {node["id"]: node for node in nodes}
                expected_directions = {}
                for edge in network["edges"]:
                    expected = set()
                    for source_id, target_id in ((edge["a"], edge["b"]), (edge["b"], edge["a"])):
                        target_agent = current_agents[by_node[target_id]["agent_id"]]
                        target_index = int(target_agent["name"].split("-")[1])
                        target_port = new_port if target_agent["id"] == transit else 24752
                        for kind in transports:
                            endpoint = next(item for item in target_agent["endpoints"] if item["transport"] == kind and urllib.parse.urlsplit(item["url"]).hostname == peer_ips[target_index] and urllib.parse.urlsplit(item["url"]).port == target_port)
                            raw = f"{edge['id']}/{source_id}/{endpoint['id']}/{peer_family}/direct"
                            expected.add(hashlib.sha256(raw.encode()).hexdigest()[:32])
                    expected_directions[edge["id"]] = expected
                def directions_recovered():
                    reports = api("GET", "/telemetry")
                    if len(reports) != 3:
                        return False
                    for current in reports:
                        for edge in network["edges"]:
                            if current["agent_id"] not in (by_node[edge["a"]]["agent_id"], by_node[edge["b"]]["agent_id"]):
                                continue
                            healthy = {link["candidate_id"] for link in current["links"] or [] if link["healthy"] and link["edge_id"] == edge["id"]}
                            if not expected_directions[edge["id"]] <= healthy:
                                return False
                    return True
                eventually(directions_recovered)
                eventually(connected)
                eventually(ping)
                eventually(exchange)
                eventually(lambda: port_free(24752, "tcp", "udp"))
                assert not original_links & {link["link_id"] for link in report()["links"] or [] if link["healthy"]}, "old listener sessions survived replacement"
                assert owned_tuns() == original_tuns, "successful listener replacement recreated the TUN"
                assert [process.pid for process in agents] == original_pids and all(process.poll() is None for process in agents), "Agent restarted during listener change"
                print("PASS: automatic retry replaces listener, republishes endpoints, restores both dialing directions/full-MTU traffic and releases old TCP/UDP ports without TUN or Agent restart", flush=True)
            if args.nat_remap:
                remapped_nodes = {node["id"] for node in nodes[:args.nat_nodes]}
                affected_edges = {edge["id"] for edge in network["edges"] if edge["a"] in remapped_nodes or edge["b"] in remapped_nodes}
                reports = api("GET", "/telemetry")
                old_links = {link["link_id"] for report in reports for link in report["links"] or [] if link["healthy"] and link["edge_id"] in affected_edges}
                unaffected_links = {link["link_id"] for report in reports for link in report["links"] or [] if link["healthy"] and link["edge_id"] not in affected_edges}
                assert old_links, "fault injection has no healthy sessions to invalidate"
                if args.nat_nodes == 1:
                    assert unaffected_links, "fixture has no healthy unaffected public edge"
                fault_started = time.monotonic()
                original_pids = [process.pid for process in agents]
                for index, router in enumerate(routers):
                    prefix = ["nsenter", "-t", str(router.pid), "-n"]
                    for rule, kind in enumerate(("udp", "tcp"), 1):
                        nat_ports[kind][index] += 4000
                        command(*prefix, "/usr/sbin/iptables", "-t", "nat", "-R", "POSTROUTING", str(rule), "-o", "eth0", "-p", kind, "--sport", "24752", "-j", "SNAT", "--to-source", f"192.0.2.{11+index}:{nat_ports[kind][index]}")
                    command(*prefix, args.conntrack, "-F")
                print("NAT fault injected: replaced TCP/UDP mapped ports and flushed router connection tracking", flush=True)
                # The controller may reconnect after its NAT state is flushed;
                # require old data sessions to actually disappear before recovery.
                def old_sessions_gone():
                    return not any(link["healthy"] and link["link_id"] in old_links for report in api("GET", "/telemetry") for link in report["links"] or [])
                eventually(old_sessions_gone, timeout=40)
                eventually(observed, timeout=90)
                expected_punch = punch_candidates()
                eventually(connected, timeout=90)
                eventually(ping)
                eventually(exchange)
                assert [process.pid for process in agents] == original_pids and all(process.poll() is None for process in agents), "Agent restarted during NAT recovery"
                healthy_links = {link["link_id"] for report in api("GET", "/telemetry") for link in report["links"] or [] if link["healthy"]}
                assert unaffected_links <= healthy_links, "remapping replaced an unaffected public-peer session"
                print(f"PASS: changed mapped ports rediscovered; both punch directions recover full-MTU traffic in {time.monotonic()-fault_started:.1f}s without Agent restart; unaffected sessions preserved", flush=True)
            if args.nat:
                stop(stun)
                eventually(ping)
                eventually(exchange)
                print("PASS: STUN outage preserves established traffic", flush=True)
            stop(server)
            eventually(ping)
            eventually(exchange)
            print("PASS: controller outage preserves native multi-hop traffic", flush=True)
            # Delete an endpoint's owned TUN while the controller is unavailable.
            # Recovery must restore address/route/MTU locally and retain peer Links.
            origin = ["nsenter", "-t", str(namespaces[0].pid), "-n"]
            links = json.loads(command(*origin, "ip", "-j", "link", "show").stdout)
            owned = [item["ifname"] for item in links if item["ifname"].startswith("gw")]
            assert len(owned) == 1, "expected one owned TUN before fault injection"
            command(*origin, "ip", "link", "delete", "dev", owned[0])
            def recovered_tun():
                links = json.loads(command(*origin, "ip", "-j", "link", "show").stdout)
                devices = [item for item in links if item["ifname"].startswith("gw")]
                return len(devices) == 1 and devices[0]["ifname"] != owned[0] and devices[0]["mtu"] == args.mtu
            eventually(recovered_tun)
            eventually(ping)
            eventually(exchange)
            print("PASS: deleted native TUN recovers its route/MTU and traffic with controller offline", flush=True)
            stop(agents[1])
            agents[1] = spawn("agent-1-restarted", agent_commands[1], dict(os.environ, HTTP_PROXY="", HTTPS_PROXY="", ALL_PROXY=""))
            eventually(ping)
            eventually(exchange)
            print("PASS: transit agent restarts from durable cache with controller offline", flush=True)
            for process in agents:
                stop(process)
            for holder in namespaces:
                links = json.loads(command("nsenter", "-t", str(holder.pid), "-n", "ip", "-j", "link", "show").stdout)
                assert not any(item["ifname"].startswith("gw") for item in links), "TUN leaked after shutdown"
            print("PASS: agent shutdown removes owned TUN interfaces", flush=True)
        except Exception:
            with contextlib.suppress(Exception):
                print("State:", json.dumps(api("GET", "/state")), flush=True)
                print("Telemetry:", json.dumps(api("GET", "/telemetry")), flush=True)
            if args.nat:
                for router in routers:
                    for table in ("filter", "nat"):
                        print(command("nsenter", "-t", str(router.pid), "-n", "/usr/sbin/iptables", "-t", table, "-L", "-nvx", check=False).stdout, flush=True)
            for path, stream in logs:
                stream.flush()
                print(f"--- {path.name} ---\n{path.read_text()[-12000:]}", flush=True)
            raise
        finally:
            for process in reversed(processes):
                if process.poll() is None:
                    process.terminate()
            for process in reversed(processes):
                with contextlib.suppress(subprocess.TimeoutExpired):
                    process.wait(timeout=5)
                if process.poll() is None:
                    process.kill()
                    process.wait()
            for _, stream in logs:
                stream.close()


if __name__ == "__main__":
    main()
