#!/usr/bin/env python3
"""Run only inside a disposable root network namespace; never modifies host routes."""
import argparse
import contextlib
import http.cookiejar
import json
import os
from pathlib import Path
import socket
import ssl
import subprocess
import tempfile
import time
import urllib.error
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
    parser.add_argument("--transport", choices=("auto", "ws", "wss", "grpc"), default="auto",
                        help="auto verifies discovered TCP/UDP; ws/wss/grpc use only manual endpoints")
    args = parser.parse_args()
    if os.geteuid() != 0 or os.readlink("/proc/self/ns/net") == os.readlink("/proc/1/ns/net"):
        raise SystemExit("Run with sudo unshare --net; an isolated network namespace is required")
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
            command("ip", "addr", "add", "192.0.2.1/24", "dev", "gw-underlay")
            command("ip", "link", "set", "gw-underlay", "up")
            namespaces = []
            for index in range(3):
                holder = spawn(f"namespace-{index}", ["unshare", "--net", "sleep", "300"])
                eventually(lambda: os.readlink(f"/proc/{holder.pid}/ns/net") != os.readlink("/proc/self/ns/net"))
                namespaces.append(holder)
                outside, inside = f"gwh{index}", f"gwn{index}"
                command("ip", "link", "add", outside, "type", "veth", "peer", "name", inside)
                command("ip", "link", "set", outside, "master", "gw-underlay")
                command("ip", "link", "set", outside, "up")
                command("ip", "link", "set", inside, "netns", str(holder.pid))
                prefix = ["nsenter", "-t", str(holder.pid), "-n"]
                command(*prefix, "ip", "link", "set", "lo", "up")
                command(*prefix, "ip", "link", "set", inside, "name", "eth0")
                command(*prefix, "ip", "addr", "add", f"192.0.2.{11+index}/24", "dev", "eth0")
                command(*prefix, "ip", "link", "set", "eth0", "up")

            password = uuid.uuid4().hex
            server = spawn("controller", [binary, "server", "--listen", "192.0.2.1:8443", "--tls-hosts", "192.0.2.1", "--data-dir", str(root / "server")], dict(os.environ, GRAPHWAN_ADMIN_PASSWORD=password))
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
                request = urllib.request.Request("https://192.0.2.1:8443/api/v1" + path, data=raw, headers=headers, method=method)
                with opener.open(request, timeout=5) as response:
                    return json.load(response)

            eventually(lambda: api("GET", "/health")["status"] == "ok")
            csrf = api("POST", "/login", {"password": password})["csrf_token"]
            agents, agent_commands = [], []
            for index, holder in enumerate(namespaces):
                token = api("POST", "/enrollment-tokens", {})["token"]
                argv = ["nsenter", "-t", str(holder.pid), "-n", binary, "agent", "--server", "https://192.0.2.1:8443", "--ca", str(ca), "--name", f"node-{index}", "--data-dir", str(root / f"agent-{index}")]
                agent_commands.append(argv)
                agents.append(spawn(f"agent-{index}", argv, dict(os.environ, GRAPHWAN_ENROLLMENT_TOKEN=token, HTTP_PROXY="", HTTPS_PROXY="", ALL_PROXY="")))
            state = eventually(lambda: (s if len(s["agents"]) == 3 and all(a["endpoints"] for a in s["agents"]) else None) if (s := api("GET", "/state")) else None)
            by_name = {a["name"]: a for a in state["agents"]}
            transports = ["udp", "tcp"] if args.transport == "auto" else [args.transport]
            if args.transport != "auto":
                for index in range(3):
                    agent = by_name[f"node-{index}"]
                    endpoint = {"id": uuid.uuid4().hex, "source": "manual", "transport": args.transport,
                                "url": f"{args.transport}://192.0.2.{11+index}:24752/custom/overlay"}
                    def configure_endpoint():
                        try:
                            return api("PATCH", f"/agents/{agent['id']}", {"manual_endpoints": [endpoint]}, api("GET", "/state")["revision"])
                        except urllib.error.HTTPError as error:
                            if error.code == 409:
                                return None
                            raise
                    eventually(configure_endpoint)
            nodes = [{"id": uuid.uuid4().hex, "agent_id": by_name[f"node-{i}"]["id"], "name": f"node-{i}", "address": f"10.42.0.{i+1}"} for i in range(3)]
            network = {"name": "native three-node test", "cidr": "10.42.0.0/24", "nodes": nodes, "edges": [{"id": uuid.uuid4().hex, "a": nodes[i]["id"], "b": nodes[i+1]["id"], "weight": 10, "enabled": True, "transports": transports, "methods": {"ipv4_direct": True, "ipv6_direct": False, "hole_punch": False}} for i in range(2)]}
            def create_network():
                try:
                    return api("POST", "/networks", network, api("GET", "/state")["revision"])
                except urllib.error.HTTPError as error:
                    if error.code == 409:
                        return None
                    raise
            eventually(create_network)
            def connected():
                reports = api("GET", "/telemetry")
                if len(reports) != 3:
                    return False
                active = {}
                for report in reports:
                    healthy = {}
                    for link in report["links"] or []:
                        if link["healthy"]:
                            healthy.setdefault(link["edge_id"], set()).add(link["transport"])
                        if link["active"]:
                            active.setdefault(link["edge_id"], []).append(link["link_id"])
                    if not healthy or any(actual != set(transports) for actual in healthy.values()):
                        return False
                return all(len(active.get(edge["id"], [])) == 2 and len(set(active[edge["id"]])) == 1 for edge in network["edges"])
            eventually(connected)
            def ping():
                return command("nsenter", "-t", str(namespaces[0].pid), "-n", "ping", "-c", "1", "-W", "1", "10.42.0.3", check=False).returncode == 0
            eventually(ping)

            echo_code = "import socket\ns=socket.socket();s.setsockopt(socket.SOL_SOCKET,socket.SO_REUSEADDR,1);s.bind(('10.42.0.3',9876));s.listen()\nwhile True:\n c,_=s.accept()\n with c:\n  while data:=c.recv(65536): c.sendall(data)\n"
            spawn("echo", ["nsenter", "-t", str(namespaces[2].pid), "-n", "python3", "-u", "-c", echo_code])
            exchange_code = "import socket\ndata=b'graphwan-end-to-end'*8192\ns=socket.create_connection(('10.42.0.3',9876),3);s.settimeout(10);s.sendall(data);out=b''\nwhile len(out)<len(data):\n chunk=s.recv(65536)\n if not chunk: raise RuntimeError('early EOF')\n out+=chunk\nassert out==data\ns.close()\n"
            def exchange():
                result = command("nsenter", "-t", str(namespaces[0].pid), "-n", "python3", "-c", exchange_code, check=False, timeout=15)
                return result.returncode == 0
            eventually(exchange)
            print(f"PASS: three native TUN agents, {transports} links, multi-hop ICMP and TCP", flush=True)
            stop(server)
            eventually(ping)
            eventually(exchange)
            print("PASS: controller outage preserves native multi-hop traffic", flush=True)
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
