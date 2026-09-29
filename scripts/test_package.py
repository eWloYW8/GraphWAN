"""Verify release contents and failure behavior using real archive formats."""

import importlib.util
import json
import os
from pathlib import Path
import tarfile
import tempfile
import unittest
import zipfile


SPEC = importlib.util.spec_from_file_location("graphwan_package", Path(__file__).with_name("package.py"))
packaging = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(packaging)


class ReleaseTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        self.builds = self.root / "builds"
        self.records = []
        for target in ("linux/amd64", "windows/arm64"):
            system, arch = target.split("/")
            relative = f"{system}-{arch}/graphwan" + (".exe" if system == "windows" else "")
            path = self.builds / relative
            path.parent.mkdir(parents=True)
            raw = ("test binary " + target).encode()
            path.write_bytes(raw)
            self.records.append({"target": target, "path": relative, "sha256": packaging.digest(raw), "bytes": len(raw)})
        self.write_manifest()

    def write_manifest(self):
        (self.builds / "manifest.json").write_text(json.dumps({"version": "abc123-dirty", "builds": self.records}))

    def package(self, name="release"):
        result = self.root / name
        packaging.package(self.builds, result, {"docs/deployment.md": (b"deployment instructions", False)}, 1700000000)
        return result

    def test_archive_contents_modes_hashes_and_reproducibility(self):
        first = self.package()
        # Source timestamps and permission bits must not leak into archives.
        for record in self.records:
            path = self.builds / record["path"]
            os.utime(path, (1800000000, 1800000000))
            path.chmod(0o600)
        second = self.package("second")
        self.assertEqual({p.name: p.read_bytes() for p in first.iterdir()}, {p.name: p.read_bytes() for p in second.iterdir()})
        metadata = json.loads((first / "release.json").read_text())
        for record in metadata["archives"]:
            path = first / record["path"]
            self.assertEqual(record["sha256"], packaging.digest(path.read_bytes()))
            self.assertEqual(record["bytes"], path.stat().st_size)
            if path.suffix == ".zip":
                with zipfile.ZipFile(path) as archive:
                    self.assertEqual(archive.read("graphwan.exe"), b"test binary windows/arm64")
                    self.assertEqual(archive.read("docs/deployment.md"), b"deployment instructions")
            else:
                with tarfile.open(path) as archive:
                    member = archive.getmember("graphwan")
                    self.assertEqual(member.mode, 0o755)
                    self.assertEqual((member.uid, member.gid, member.mtime), (0, 0, 1700000000))
                    self.assertEqual(archive.extractfile(member).read(), b"test binary linux/amd64")
                    self.assertEqual(archive.getmember("docs/deployment.md").mode, 0o644)
        for line in (first / "SHA256SUMS").read_text().splitlines():
            expected, name = line.split("  ", 1)
            self.assertEqual(expected, packaging.digest((first / name).read_bytes()))

    def test_corrupt_binary_never_publishes_partial_release(self):
        (self.builds / self.records[-1]["path"]).write_bytes(b"corruption")
        with self.assertRaisesRegex(ValueError, "size/hash mismatch"):
            self.package()
        self.assertFalse((self.root / "release").exists())
        self.assertFalse(list(self.root.glob(".graphwan-package-*")))

    def test_existing_release_is_preserved(self):
        first = self.package()
        checksum = (first / "SHA256SUMS").read_bytes()
        with self.assertRaisesRegex(ValueError, "already exists"):
            self.package()
        self.assertEqual(checksum, (first / "SHA256SUMS").read_bytes())

    def test_duplicate_targets_and_manifest_path_escape_are_rejected(self):
        original = self.records[0]["path"]
        self.records[0]["path"] = "../../outside"
        self.write_manifest()
        with self.assertRaisesRegex(ValueError, "unexpected binary path"):
            self.package()
        self.records[0]["path"] = original
        self.records.append(self.records[0])
        self.write_manifest()
        with self.assertRaisesRegex(ValueError, "duplicate target"):
            self.package()
        self.assertFalse((self.root / "release").exists())


if __name__ == "__main__":
    unittest.main()
