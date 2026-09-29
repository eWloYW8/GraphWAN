#!/usr/bin/env python3
"""Fetch an unchanged, pinned Wintun DLL and its distribution license."""

import argparse
import hashlib
import io
import os
from pathlib import Path
import tempfile
import urllib.request
import zipfile


VERSION = "0.14.1"
URL = f"https://www.wintun.net/builds/wintun-{VERSION}.zip"
SHA256 = "07c256185d6ee3652e09fa55c0b673e2624b565e02c4b9091c79ca7d2f24ef51"
MAX_ARCHIVE = 8 * 1024 * 1024
ARCHITECTURES = {"amd64": "amd64", "arm64": "arm64", "386": "x86", "arm": "arm"}


def install(archive, arch, output):
    if len(archive) > MAX_ARCHIVE or hashlib.sha256(archive).hexdigest() != SHA256:
        raise ValueError("Wintun archive SHA-256 mismatch; no files installed")
    # Select exact members; never extract archive paths to the filesystem.
    with zipfile.ZipFile(io.BytesIO(archive)) as bundle:
        files = {
            "WINTUN-LICENSE.txt": bundle.read("wintun/LICENSE.txt"),
            "wintun.dll": bundle.read(f"wintun/bin/{ARCHITECTURES[arch]}/wintun.dll"),
        }
    output = Path(output)
    output.mkdir(parents=True, exist_ok=True)
    # Publish the license first. Each replacement is atomic on its filesystem.
    for name, data in files.items():
        temp_name = None
        try:
            with tempfile.NamedTemporaryFile(dir=output, delete=False) as temp:
                temp_name = temp.name
                temp.write(data)
                temp.flush()
                os.fsync(temp.fileno())
            os.replace(temp_name, output / name)
        finally:
            if temp_name is not None and os.path.exists(temp_name):
                os.unlink(temp_name)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--arch", required=True, choices=ARCHITECTURES)
    parser.add_argument("--output", required=True, type=Path, help="directory containing graphwan.exe")
    parser.add_argument("--archive", type=Path, help="use a local official ZIP, with the same checksum check")
    args = parser.parse_args()
    try:
        if args.archive is not None:
            with args.archive.open("rb") as source:
                archive = source.read(MAX_ARCHIVE + 1)
        else:
            with urllib.request.urlopen(URL, timeout=60) as response:
                archive = response.read(MAX_ARCHIVE + 1)
        install(archive, args.arch, args.output)
    except (OSError, ValueError, KeyError, zipfile.BadZipFile) as error:
        parser.exit(1, f"Wintun installation failed: {error}\n")
    print(f"Installed Wintun {VERSION} ({args.arch}) and license in {args.output}")


if __name__ == "__main__":
    main()
