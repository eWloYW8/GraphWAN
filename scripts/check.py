#!/usr/bin/env python3
"""Reproduce GraphWAN's CI checks without changing the developer's host network."""

import argparse
import json
import os
from pathlib import Path
import platform
import shlex
import shutil
import subprocess
import sys
import tempfile


ROOT = Path(__file__).resolve().parents[1]
NATIVE = {
    "linux": {
        "tunnel": ["TestNativeLinuxTunnel"],
        "discovery": ["TestNativeInterfaceDiscovery"],
    },
    "darwin": {
        "tunnel": ["TestNativeDarwinTunnel", "TestNativeDarwinConfiguration", "TestNativeDarwinRouteConflict"],
        "agent": ["TestNativeBSDConfigurationReconcile"],
    },
    "windows": {
        "tunnel": ["TestNativeWindowsTunnel", "TestNativeWindowsConfiguration", "TestNativeWindowsNameOwnership", "TestNativeWindowsRouteOwnership"],
        "agent": ["TestNativeWindowsConfigurationReconcile"],
    },
    "freebsd": {
        "tunnel": ["TestNativeFreeBSDTunnel", "TestFreeBSDReplacement", "TestFreeBSDAddressReconfigure", "TestFreeBSDFallbackCleanup", "TestFreeBSDCrashCleanup"],
        "agent": ["TestNativeBSDConfigurationReconcile"],
        "discovery": ["TestNativeFreeBSDInterfaceDiscovery"],
    },
    "openbsd": {
        "tunnel": ["TestNativeOpenBSDTunnel", "TestNativeOpenBSDConfiguration", "TestNativeOpenBSDRouteConflict", "TestNativeOpenBSDOwnership", "TestNativePersistentBSDCrashRecovery", "TestNativePersistentBSDRecoveryOwnership", "TestNativePersistentBSDRecoveryWithoutOpen", "TestNativePersistentBSDRecoveryIncompleteRecord", "TestNativePersistentBSDRecoveryRecordProtection", "TestNativePersistentBSDRecoveryConcurrentClose"],
        "agent": ["TestNativeBSDConfigurationReconcile"],
        "discovery": ["TestNativeOpenBSDInterfaceDiscovery"],
    },
    "netbsd": {
        "tunnel": ["TestNetBSDIOCTLLayout", "TestNativeNetBSDTunnel", "TestNativeNetBSDConfiguration", "TestNativeNetBSDRouteConflict", "TestNativeNetBSDOwnership", "TestNativeNetBSDMTULimit", "TestNativePersistentBSDCrashRecovery", "TestNativePersistentBSDRecoveryOwnership", "TestNativePersistentBSDRecoveryWithoutOpen", "TestNativePersistentBSDRecoveryIncompleteRecord", "TestNativePersistentBSDRecoveryRecordProtection", "TestNativePersistentBSDRecoveryConcurrentClose"],
        "agent": ["TestNativeBSDConfigurationReconcile"],
        "discovery": ["TestNativeNetBSDInterfaceDiscovery"],
    },
}
OPT_IN = {"darwin": "GRAPHWAN_TEST_MACOS", "windows": "GRAPHWAN_TEST_WINDOWS", "freebsd": "GRAPHWAN_TEST_VM", "openbsd": "GRAPHWAN_TEST_VM", "netbsd": "GRAPHWAN_TEST_VM"}
SOCKET_GATES = {
    "transport": [
        "TestUDPWildcardReplySource", "TestTCPWildcardBothFamiliesAndClose",
        "TestQUICWildcardBothFamilies", "TestSTUNWildcardBothFamilies",
        "TestTCPSTUNWildcardDataPortBothFamilies", "TestWildcardBindCollisionCleanup",
        "TestWildcardBindUnexpectedFailureCleanup", "TestUDPWildcardConnectionBoundBothFamilies",
    ],
    # Run the complete Mesh package; these mandatory roots also catch missing
    # loopback fixtures that would otherwise turn the important cases into skips.
    "mesh": ["TestDNSAllAddressesRetainedAcrossTransports", "TestPolicyEditsPreserveUnaffectedLinks", "TestRealUDPFailureFallsBackToExistingTCP"],
}
# FreeBSD prints a separate netmask; NetBSD includes /prefix in the address.
IPV4_ADDRESS_PRESENT = '$1 == "inet" {split($2, parts, "/"); if (parts[1] == address) found=1} END {exit !found}'
BSD_CI = {system: {**NATIVE[system], **SOCKET_GATES} for system in ("freebsd", "netbsd")}


def executable(name):
    found = shutil.which(name)
    if found is None:
        raise RuntimeError(f"required executable not found: {name}")
    return found


def run(args, *, cwd=ROOT, env=None, log=None):
    args = [str(arg) for arg in args]
    print("+ " + shlex.join(args), flush=True)
    completed = subprocess.run(args, cwd=cwd, env=env, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True)
    if log is not None:
        log.parent.mkdir(parents=True, exist_ok=True)
        log.write_text(completed.stdout, encoding="utf-8")
    print(completed.stdout, end="", flush=True)
    if completed.returncode:
        raise RuntimeError(f"command exited with status {completed.returncode}: {shlex.join(args)}")
    return completed.stdout


def tracked(pattern):
    raw = subprocess.check_output(["git", "ls-files", "-z", "--", pattern], cwd=ROOT)
    return [item for item in raw.decode().split("\0") if item]


def check_go(logs):
    files = tracked("*.go")
    unformatted = []
    for start in range(0, len(files), 100):
        output = run([executable("gofmt"), "-l", *files[start:start + 100]])
        unformatted.extend(output.splitlines())
    if unformatted:
        raise RuntimeError("run gofmt on the files listed above")
    go = executable("go")
    run([go, "mod", "verify"], log=logs / "modules.log")
    run([go, "vet", "./..."], log=logs / "vet.log")
    module = run([go, "list", "-m"]).strip()
    output = run([go, "test", "-race", "-count=1", "-json", "./..."], log=logs / "go-tests.jsonl")
    events = [json.loads(line) for line in output.splitlines()]
    for package, expected in SOCKET_GATES.items():
        selected = [json.dumps(event) for event in events if event.get("Package") == f"{module}/internal/{package}"]
        require_native_pass("\n".join(selected), expected)


def check_frontend(logs):
    pnpm = executable("pnpm")
    for name, args in [
        ("install", ["install", "--frozen-lockfile"]),
        ("format", ["format:check"]),
        ("build", ["build"]),
        ("unit", ["test"]),
        ("browser", ["test:e2e"]),
    ]:
        run([pnpm, "--dir", "web", *args], log=logs / f"web-{name}.log")
        if name == "build":
            # git diff alone would miss new hashed assets (including ignored
            # files). Compare file sets as well as tracked contents.
            expected = set(tracked("internal/webui/dist/**"))
            actual = {p.relative_to(ROOT).as_posix() for p in (ROOT / "internal/webui/dist").rglob("*") if p.is_file()}
            if expected != actual:
                raise RuntimeError(f"embedded asset file set differs: {sorted(expected ^ actual)}; commit the rebuilt assets")
            run(["git", "diff", "--exit-code", "--", "internal/webui/dist"], log=logs / "web-assets.log")


def check_deployment(logs):
    """Check Linux unit syntax without installing a service on the host."""
    if platform.system() != "Linux":
        raise RuntimeError("deployment unit verification requires Linux")
    with tempfile.TemporaryDirectory(prefix="graphwan-deployment-") as temporary:
        directory = Path(temporary)
        binary = directory / "graphwan"
        run([executable("go"), "build", "-o", binary, "./cmd/graphwan"], log=logs / "deployment-build.log")
        units = []
        for source in sorted((ROOT / "deploy/linux").glob("*.service")):
            raw = source.read_text(encoding="utf-8")
            command = "ExecStart=/usr/local/bin/graphwan "
            if raw.count(command) != 1:
                raise RuntimeError(f"unexpected deployment executable in {source}")
            unit = directory / source.name
            unit.write_text(raw.replace(command, f'ExecStart="{binary}" '), encoding="utf-8")
            units.append(unit)
        if len(units) != 2:
            raise RuntimeError("expected controller and Agent service units")
        run([executable("systemd-analyze"), "verify", "--man=no", *units], log=logs / "systemd-verify.log")


def require_native_pass(output, expected, *, complete=False):
    """A successful test binary exit must not turn absent/skipped gates green."""
    results = {}
    skipped = []
    package_result = None
    failed = []
    for line in output.splitlines():
        event = json.loads(line)
        test, action = event.get("Test"), event.get("Action")
        if not test and action in ("pass", "fail", "skip"):
            package_result = action
        if action == "fail":
            failed.append(test or "package")
        if test and action in ("pass", "fail", "skip"):
            results[test] = action
            if action == "skip" and (complete or any(test == name or test.startswith(name + "/") for name in expected)):
                skipped.append(test)
    missing = [name for name in expected if results.get(name) != "pass"]
    if missing or skipped or failed or package_result != "pass":
        raise RuntimeError(f"native gates did not execute successfully: missing/not passed={missing}, skipped={skipped}, failed={failed}, package={package_result}")


def prepare_bsd(system, directory, logs):
    """Build on the host; disposable BSD guests need only their base shell."""
    system_name = {"freebsd": "FreeBSD", "netbsd": "NetBSD"}[system]
    gates = BSD_CI[system]
    directory.mkdir(parents=True, exist_ok=True)
    script = directory / "run.sh"
    script.unlink(missing_ok=True)
    env = os.environ.copy()
    env.update(GOOS=system, GOARCH="amd64", GOAMD64="v1", CGO_ENABLED="0", GOFLAGS="", GOEXPERIMENT="", GOWORK="off")
    go = executable("go")
    run([go, "build", "-trimpath", "-o", directory / "test2json", "cmd/test2json"], env=env, log=logs / "test2json-build.log")
    for package in gates:
        run([go, "test", "-c", "-trimpath", "-tags", "integration", "-o", directory / (package + ".test"), "./internal/" + package], env=env, log=logs / f"native-{package}-build.log")
    # Refuse pre-existing fixture addresses rather than adopting or removing
    # somebody else's aliases. Cleanup tracks successful additions individually.
    lines = ["""#!/bin/sh
set -eu
cd "$(dirname "$0")"
test "$(uname -s)" = __GRAPHWAN_BSD__
test "$(id -u)" = 0
test "${GRAPHWAN_TEST_VM:-}" = 1
__GRAPHWAN_FIXTURE__
interfaces=$(/sbin/ifconfig -a)
for address in 127.0.0.2 127.0.0.3; do
    if printf '%s\\n' "$interfaces" | awk -v address="$address" __GRAPHWAN_ADDRESS_PROBE__; then
        echo "Fixture address already assigned: $address" >&2
        exit 1
    fi
done
alias2=0
alias3=0
cleanup() {
    status=$?
    trap - EXIT
    if [ "$alias2" = 1 ]; then /sbin/ifconfig lo0 inet 127.0.0.2 -alias || status=1; fi
    if [ "$alias3" = 1 ]; then /sbin/ifconfig lo0 inet 127.0.0.3 -alias || status=1; fi
    exit "$status"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
/sbin/ifconfig lo0 inet 127.0.0.2/32 alias
alias2=1
/sbin/ifconfig lo0 inet 127.0.0.3/32 alias
alias3=1
mkdir -p logs
status=0
""".replace("__GRAPHWAN_BSD__", system_name).replace("__GRAPHWAN_ADDRESS_PROBE__", shlex.quote(IPV4_ADDRESS_PRESENT)).replace("__GRAPHWAN_FIXTURE__",
    ': "${GRAPHWAN_TEST_INTERFACE:?set a spare guest interface for discovery}"' if system == "netbsd" else "")]
    for package, expected in gates.items():
        tests = "." if package in SOCKET_GATES else "^(" + "|".join(expected) + ")$"
        command = shlex.join(["./test2json", "-t", "-p", package, f"./{package}.test", "-test.v", "-test.run", tests, "-test.timeout=300s"])
        lines.append(f"{command} > logs/native-{package}.jsonl 2>&1 || status=1\ncat logs/native-{package}.jsonl\n")
    lines.append('exit "$status"\n')
    script.write_text("".join(lines), encoding="utf-8")


def verify_bsd(system, logs):
    for package, expected in BSD_CI[system].items():
        require_native_pass((logs / f"native-{package}.jsonl").read_text(encoding="utf-8"), expected, complete=package in SOCKET_GATES)
    print(f"{system} native kernel, Agent, discovery and transport gates passed", flush=True)


def check_native(logs):
    system = platform.system().lower()
    if system not in NATIVE:
        raise RuntimeError(f"no native gate defined for {system}")
    if system in OPT_IN and os.environ.get(OPT_IN[system]) != "1":
        raise RuntimeError(f"native tests change interfaces/routes; use a disposable host and set {OPT_IN[system]}=1")
    go = executable("go")
    env = os.environ.copy()
    with tempfile.TemporaryDirectory(prefix="graphwan-native-") as temp:
        directory = Path(temp)
        if system == "windows":
            arch = run([go, "env", "GOARCH"]).strip()
            run([sys.executable, ROOT / "scripts/fetch-wintun.py", "--arch", arch, "--output", directory])
        for package, expected in NATIVE[system].items():
            binary = directory / (package + (".test.exe" if system == "windows" else ".test"))
            compile_args = [go, "test", "-c", "-tags", "integration", "-o", binary]
            if system == "linux":
                compile_args.append("-race")
            run([*compile_args, "./internal/" + package], log=logs / f"native-{package}-build.log")
            tests = "^(" + "|".join(expected) + ")$"
            args = [go, "tool", "test2json", "-t", "-p", package, binary, "-test.v", "-test.run", tests, "-test.timeout=180s"]
            privilege = [] if system != "windows" and os.geteuid() == 0 else ["sudo", "-n"]
            if system == "linux":
                args = [*privilege, "unshare", "--net", "env", "GRAPHWAN_TEST_NETNS=1", *args]
            elif system in ("darwin", "freebsd", "openbsd", "netbsd"):
                args = [*privilege, "env", OPT_IN[system] + "=1", *args]
            output = run(args, env=env, log=logs / f"native-{package}.jsonl")
            require_native_pass(output, expected)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("check", choices=("go", "frontend", "deployment", "native", "prepare-freebsd", "verify-freebsd", "prepare-netbsd", "verify-netbsd"))
    parser.add_argument("--logs", type=Path, required=True, help="directory to retain check output")
    parser.add_argument("--binaries", type=Path, help="output bundle for prepare-freebsd/prepare-netbsd; copy this directory to the disposable guest")
    args = parser.parse_args()
    try:
        if args.check.startswith("prepare-"):
            if args.binaries is None:
                parser.error(f"{args.check} requires --binaries")
            prepare_bsd(args.check.removeprefix("prepare-"), args.binaries.resolve(), args.logs.resolve())
        elif args.check.startswith("verify-"):
            verify_bsd(args.check.removeprefix("verify-"), args.logs.resolve())
        else:
            {"go": check_go, "frontend": check_frontend, "deployment": check_deployment, "native": check_native}[args.check](args.logs.resolve())
    except (OSError, RuntimeError, subprocess.SubprocessError, json.JSONDecodeError) as error:
        parser.exit(1, f"GraphWAN check failed: {error}\n")


if __name__ == "__main__":
    main()
