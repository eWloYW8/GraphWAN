"""Regression tests for CI's native-execution gate, including skipped subtests."""

import importlib.util
import json
from pathlib import Path
import unittest

spec = importlib.util.spec_from_file_location("checks", Path(__file__).with_name("check.py"))
checks = importlib.util.module_from_spec(spec)
spec.loader.exec_module(checks)


class NativeEvidenceTests(unittest.TestCase):
    def test_required_tests_and_children_execute(self):
        events = [{"Action": "pass", "Test": "TestNative/ipv6"}, {"Action": "pass", "Test": "TestNative"}, {"Action": "skip", "Test": "Unrelated"}, {"Action": "pass"}]
        checks.require_native_pass("\n".join(map(json.dumps, events)), ["TestNative"])

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


if __name__ == "__main__":
    unittest.main()
