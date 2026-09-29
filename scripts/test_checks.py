"""Regression tests for CI's native-execution gate, including skipped subtests."""

import importlib.util
import json
import subprocess
import shutil
from pathlib import Path
import unittest

spec = importlib.util.spec_from_file_location("checks", Path(__file__).with_name("check.py"))
checks = importlib.util.module_from_spec(spec)
spec.loader.exec_module(checks)


class NativeEvidenceTests(unittest.TestCase):
    def test_required_tests_and_children_execute(self):
        events = [{"Action": "pass", "Test": "TestNative/ipv6"}, {"Action": "pass", "Test": "TestNative"}, {"Action": "skip", "Test": "Unrelated"}, {"Action": "pass"}]
        checks.require_native_pass("\n".join(map(json.dumps, events)), ["TestNative"])

    def test_complete_suite_rejects_skips_outside_named_roots(self):
        events = [{"Action": "pass", "Test": "TestNative"}, {"Action": "skip", "Test": "Other"}, {"Action": "pass"}]
        with self.assertRaises(RuntimeError):
            checks.require_native_pass("\n".join(map(json.dumps, events)), ["TestNative"], complete=True)

    def test_successful_package_exit_cannot_hide_missing_or_skipped_test(self):
        for events in [
            [{"Action": "pass"}],
            [{"Action": "skip", "Test": "TestNative"}, {"Action": "pass"}],
            [{"Action": "fail", "Test": "TestNative"}],
            [{"Action": "skip", "Test": "TestNative/ipv6"}, {"Action": "pass", "Test": "TestNative"}, {"Action": "pass"}],
            [{"Action": "pass", "Test": "TestNative"}],
            [{"Action": "pass", "Test": "TestNative"}, {"Action": "fail"}],
            [{"Action": "fail", "Test": "Other"}, {"Action": "pass", "Test": "TestNative"}, {"Action": "pass"}],
        ]:
            with self.subTest(events=events), self.assertRaises(RuntimeError):
                checks.require_native_pass("\n".join(map(json.dumps, events)), ["TestNative"])


@unittest.skipUnless(shutil.which("awk"), "BSD runner fixture parsing requires awk")
class BSDAddressFixtureTests(unittest.TestCase):
    def test_existing_fixture_addresses_in_both_native_formats(self):
        for line in ["inet 127.0.0.2 netmask 0xffffffff", "inet 127.0.0.2/32 flags 0"]:
            with self.subTest(line=line):
                result = subprocess.run(["awk", "-v", "address=127.0.0.2", checks.IPV4_ADDRESS_PRESENT], input=line+"\n", text=True)
                self.assertEqual(result.returncode, 0)

    def test_other_addresses_do_not_claim_the_fixture(self):
        for line in ["inet 127.0.0.20/32 flags 0", "inet 127.0.0.1 netmask 0xff000000", "inet6 ::ffff:127.0.0.2/128"]:
            with self.subTest(line=line):
                result = subprocess.run(["awk", "-v", "address=127.0.0.2", checks.IPV4_ADDRESS_PRESENT], input=line+"\n", text=True)
                self.assertEqual(result.returncode, 1)


if __name__ == "__main__":
    unittest.main()
