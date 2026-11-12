"""Ensure the CI vector gate cannot silently accept zero tests or skips."""

import json
import unittest

from check_vectors import require_passes


class VectorExecutionGateTests(unittest.TestCase):
    def output(self, *events):
        return "\n".join(json.dumps({"Package": "candidate", **event}) for event in events)

    def test_requires_each_named_test_and_package_to_pass(self):
        require_passes(self.output(
            {"Action": "pass", "Test": "TestVector"}, {"Action": "pass"}
        ), "candidate", ["TestVector"])
        for events in (
            ({"Action": "pass"},),  # go test -run typo exits zero
            ({"Action": "skip", "Test": "TestVector"}, {"Action": "pass"}),
            ({"Action": "pass", "Test": "TestVector/subcase"}, {"Action": "pass"}),
            ({"Action": "pass", "Test": "TestVector"},),  # truncated stream
            ({"Action": "pass", "Test": "TestVector"}, {"Action": "fail"}),
            ({"Action": "pass", "Test": "TestVector", "Package": "unrelated"},
             {"Action": "pass"}),
        ):
            with self.subTest(events=events), self.assertRaises(RuntimeError):
                require_passes(self.output(*events), "candidate", ["TestVector"])


if __name__ == "__main__":
    unittest.main()
