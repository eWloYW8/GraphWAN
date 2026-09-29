#!/usr/bin/env python3
"""Package verified cross-build outputs, documentation and deployment examples."""

import argparse
import datetime
import gzip
import hashlib
import io
import json
import os
from pathlib import Path
import re
import subprocess
import tarfile
import tempfile
import zipfile


ROOT = Path(__file__).resolve().parents[1]


def digest(data):
    return hashlib.sha256(data).hexdigest()


def archive(path, files, epoch, windows):
    """Normalize entry order, timestamps, ownership and modes."""
    if windows:
        date = datetime.datetime.fromtimestamp(max(epoch, 315532800), datetime.timezone.utc)
        with zipfile.ZipFile(path, "w", compression=zipfile.ZIP_DEFLATED, compresslevel=9) as output:
            for name, (data, executable) in sorted(files.items()):
                entry = zipfile.ZipInfo(name, date.timetuple()[:6])
                entry.create_system = 3
                entry.external_attr = (0o100755 if executable else 0o100644) << 16
                entry.compress_type = zipfile.ZIP_DEFLATED
                output.writestr(entry, data, compresslevel=9)
    else:
        with path.open("wb") as raw, gzip.GzipFile(filename="", mode="wb", fileobj=raw, mtime=epoch) as compressed:
            with tarfile.open(fileobj=compressed, mode="w", format=tarfile.PAX_FORMAT) as output:
                for name, (data, executable) in sorted(files.items()):
                    entry = tarfile.TarInfo(name)
                    entry.size, entry.mtime = len(data), epoch
                    entry.mode = 0o755 if executable else 0o644
                    output.addfile(entry, io.BytesIO(data))


def package(builds, destination, common, epoch):
    """Reject mismatched binaries and publish the whole directory atomically."""
    if not 0 <= epoch <= 0xFFFFFFFF:
        raise ValueError("archive epoch must fit an unsigned 32-bit timestamp")
    manifest_raw = (builds / "manifest.json").read_bytes()
    manifest = json.loads(manifest_raw)
    version = manifest["version"]
    if not isinstance(version, str) or not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9._-]{0,79}", version):
        raise ValueError("invalid build version")
    if not isinstance(manifest["builds"], list) or not manifest["builds"]:
        raise ValueError("build manifest is empty")
    if destination.exists():
        raise ValueError("output directory already exists; choose a new directory")
    destination.parent.mkdir(parents=True, exist_ok=True)
    records, seen = [], set()
    with tempfile.TemporaryDirectory(prefix=".graphwan-package-", dir=destination.parent) as temporary:
        stage = Path(temporary) / "release"
        stage.mkdir()
        for build in manifest["builds"]:
            target = build["target"]
            if not isinstance(target, str) or not re.fullmatch(r"(linux|windows|darwin|freebsd|openbsd|netbsd|dragonfly)/[a-z0-9]+", target) or target in seen:
                raise ValueError(f"invalid or duplicate target: {target}")
            seen.add(target)
            system, arch = target.split("/")
            executable = "graphwan.exe" if system == "windows" else "graphwan"
            relative = f"{system}-{arch}/{executable}"
            if build["path"] != relative:
                raise ValueError(f"unexpected binary path for {target}")
            source = builds / relative
            if source.is_symlink() or source.parent.is_symlink():
                raise ValueError(f"binary must not be a symlink: {relative}")
            binary = source.read_bytes()
            if len(binary) != build["bytes"] or digest(binary) != build["sha256"]:
                raise ValueError(f"binary size/hash mismatch: {target}")
            files = {**common, executable: (binary, True), "build-manifest.json": (manifest_raw, False)}
            name = f"graphwan-{version}-{system}-{arch}" + (".zip" if system == "windows" else ".tar.gz")
            archive(stage / name, files, epoch, system == "windows")
            raw = (stage / name).read_bytes()
            records.append({"target": target, "path": name, "bytes": len(raw), "sha256": digest(raw)})
        metadata = {"version": version, "source_date_epoch": epoch, "build_manifest_sha256": digest(manifest_raw), "documents": {name: digest(data) for name, (data, _) in sorted(common.items())}, "archives": records}
        (stage / "release.json").write_text(json.dumps(metadata, indent=2) + "\n", encoding="utf-8")
        checksums = {record["path"]: record["sha256"] for record in records}
        checksums["release.json"] = digest((stage / "release.json").read_bytes())
        (stage / "SHA256SUMS").write_text("".join(f"{checksum}  {name}\n" for name, checksum in sorted(checksums.items())), encoding="utf-8")
        os.rename(stage, destination)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--builds", required=True, type=Path, help="cross-build.py output containing manifest.json")
    parser.add_argument("--output", required=True, type=Path, help="new directory; never overwrites an existing release")
    args = parser.parse_args()
    try:
        epoch = int(os.environ.get("SOURCE_DATE_EPOCH") or subprocess.check_output(["git", "show", "-s", "--format=%ct", "HEAD"], cwd=ROOT, text=True).strip())
        names = subprocess.check_output(["git", "ls-files", "-z", "--", "README.md", "docs/*.md", "deploy/*", "scripts/fetch-wintun.py"], cwd=ROOT).decode().split("\0")
        common = {}
        for name in filter(None, names):
            path = ROOT / name
            if path.is_symlink() or not path.is_file():
                raise ValueError(f"package document must be a regular file: {name}")
            common[name] = (path.read_bytes(), False)
        package(args.builds.resolve(), args.output.absolute(), common, epoch)
    except (OSError, ValueError, KeyError, TypeError, subprocess.SubprocessError) as error:
        parser.exit(1, f"Packaging failed: {error}\n")
    print(f"Packaged binaries and checksums in {args.output}")


if __name__ == "__main__":
    main()
