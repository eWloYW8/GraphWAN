#!/usr/bin/env python3
"""Compile controller/Agent binaries for Go's advertised targets; this is not native acceptance."""

import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import subprocess
import sys


ROOT = Path(__file__).resolve().parents[1]
SYSTEMS = ("linux", "windows", "darwin", "freebsd", "openbsd", "netbsd", "dragonfly")
BASELINES = {
    "GOAMD64": "v1", "GO386": "sse2", "GOARM": "7", "GOARM64": "v8.0",
    "GOMIPS": "softfloat", "GOMIPS64": "softfloat", "GOPPC64": "power8",
    "GORISCV64": "rva20u64",
}
BUILD_ENV = {
    "CGO_ENABLED": "0", "GOENV": "off", "GO111MODULE": "on",
    "GOFLAGS": "", "GOEXPERIMENT": "", "GOWORK": "off",
    "GOFIPS140": "off", "GOTOOLCHAIN": "local",
}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--goos", choices=SYSTEMS, action="append", help="repeat to select systems; default: all")
    parser.add_argument("--target", action="append", help="limit to explicit goos/goarch pairs")
    parser.add_argument("--output", required=True, type=Path)
    args = parser.parse_args()
    try:
        build_env = os.environ.copy()
        build_env.update(BUILD_ENV)
        build_env.update(BASELINES)
        # Inventory and version probes use the same toolchain/configuration as
        # compilation, independent of go env -w and any surrounding workspace.
        build_env.pop("GOOS", None)
        build_env.pop("GOARCH", None)
        available = subprocess.check_output(["go", "tool", "dist", "list"], cwd=ROOT, env=build_env, text=True).splitlines()
        systems = args.goos or SYSTEMS
        targets = sorted(target for target in available if target.split("/")[0] in systems)
        if args.target:
            unknown = set(args.target) - set(targets)
            if unknown:
                raise ValueError(f"targets not advertised by this Go toolchain: {sorted(unknown)}")
            targets = sorted(set(args.target))
        revision = subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=ROOT, text=True).strip()
        if not re.fullmatch(r"[0-9a-f]{40,64}", revision):
            raise ValueError("invalid Git revision")
        dirty = bool(subprocess.check_output(["git", "status", "--porcelain", "--untracked-files=normal"], cwd=ROOT))
        version = revision[:12] + ("-dirty" if dirty else "")
        args.output.mkdir(parents=True, exist_ok=True)
        manifest_path = args.output / "manifest.json"
        manifest_path.unlink(missing_ok=True)
        records = []
        for target in targets:
            goos, goarch = target.split("/")
            directory = args.output / f"{goos}-{goarch}"
            directory.mkdir(parents=True, exist_ok=True)
            binary = directory / ("graphwan.exe" if goos == "windows" else "graphwan")
            env = {**build_env, "GOOS": goos, "GOARCH": goarch}
            print(f"Building {target}", flush=True)
            subprocess.run(["go", "build", "-trimpath", "-buildvcs=false", "-ldflags", f"-s -w -X main.version={version}", "-o", str(binary.resolve()), "./cmd/graphwan"], cwd=ROOT, env=env, check=True)
            records.append({"target": target, "path": binary.relative_to(args.output).as_posix(), "sha256": hashlib.sha256(binary.read_bytes()).hexdigest(), "bytes": binary.stat().st_size})
        manifest = {"revision": revision, "worktree_dirty": dirty, "version": version, "go": subprocess.check_output(["go", "version"], cwd=ROOT, env=build_env, text=True).strip(), "cgo": False, "architecture_baselines": BASELINES, "go_environment": BUILD_ENV, "builds": records}
        manifest_path.write_text(json.dumps(manifest, indent=2) + "\n", encoding="utf-8")
    except (OSError, ValueError, subprocess.SubprocessError) as error:
        parser.exit(1, f"Cross-build failed: {error}\n")


if __name__ == "__main__":
    main()
