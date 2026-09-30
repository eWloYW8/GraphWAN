#!/usr/bin/env python3
"""Prepare one verified standalone release binary for each cross-build target."""

import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import tempfile


def package(builds, destination):
    """Verify the build manifest and publish only binaries, atomically."""
    manifest = json.loads((builds / "manifest.json").read_bytes())
    version = manifest["version"]
    if not isinstance(version, str) or not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9._+-]{0,79}", version):
        raise ValueError("invalid build version")
    if not isinstance(manifest["builds"], list) or not manifest["builds"]:
        raise ValueError("build manifest is empty")
    if destination.exists():
        raise ValueError("output directory already exists; choose a new directory")
    destination.parent.mkdir(parents=True, exist_ok=True)
    seen = set()
    with tempfile.TemporaryDirectory(prefix=".graphwan-package-", dir=destination.parent) as temporary:
        stage = Path(temporary) / "release"
        stage.mkdir()
        for build in manifest["builds"]:
            target = build["target"]
            if not isinstance(target, str) or not re.fullmatch(r"(linux|windows|darwin|freebsd|openbsd|netbsd|dragonfly)/[a-z0-9]+", target) or target in seen:
                raise ValueError(f"invalid or duplicate target: {target}")
            seen.add(target)
            system, arch = target.split("/")
            suffix = ".exe" if system == "windows" else ""
            relative = f"{system}-{arch}/graphwan{suffix}"
            if build["path"] != relative:
                raise ValueError(f"unexpected binary path for {target}")
            source = builds / relative
            if source.is_symlink() or source.parent.is_symlink():
                raise ValueError(f"binary must not be a symlink: {relative}")
            binary = source.read_bytes()
            if len(binary) != build["bytes"] or hashlib.sha256(binary).hexdigest() != build["sha256"]:
                raise ValueError(f"binary size/hash mismatch: {target}")
            output = stage / f"graphwan-{version}-{system}-{arch}{suffix}"
            output.write_bytes(binary)
            output.chmod(0o755)
        os.rename(stage, destination)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--builds", required=True, type=Path, help="cross-build.py output containing manifest.json")
    parser.add_argument("--output", required=True, type=Path, help="new directory; never overwrites an existing release")
    args = parser.parse_args()
    try:
        package(args.builds.resolve(), args.output.absolute())
    except (OSError, ValueError, KeyError, TypeError) as error:
        parser.exit(1, f"Release preparation failed: {error}\n")
    print(f"Prepared standalone binaries in {args.output}")


if __name__ == "__main__":
    main()
