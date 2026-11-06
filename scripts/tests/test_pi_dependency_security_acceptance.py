"""Keep every Pi HTTP-client copy outside the reported advisory range.

GitHub Dependabot alerts 1–8, read 2026-11-06, all report undici
>=8.0.0,<8.12.2. The GHSA identifiers below preserve that provenance;
the release need not pin one particular patched version.
"""

import json
from pathlib import Path
import re
import unittest

ROOT = Path(__file__).resolve().parents[2]
ADVISORIES = (
    "GHSA-4w7w-5xrc-9h26", "GHSA-2m5v-pch6-74hx", "GHSA-qrxm-fqhp-jwvm",
    "GHSA-xjj6-f33q-929c", "GHSA-9fm3-97px-rjf6", "GHSA-55wc-3pg8-jfw6",
    "GHSA-4qv8-2p9j-2685", "GHSA-f72v-6w45-q472",
)


def undici_copies(packages):
    return {path: package for path, package in packages.items()
            if path == "node_modules/undici" or path.endswith("/node_modules/undici")
            or package.get("name") == "undici"}


def vulnerable_copies(packages):
    affected = []
    for path, package in undici_copies(packages).items():
        version = package.get("version", "")
        match = re.fullmatch(r"(\d+)\.(\d+)\.(\d+)(?:\+[A-Za-z0-9.-]+)?", version)
        if not match:
            raise ValueError(f"cannot establish a stable undici release: {path} version={version!r}")
        if (8, 0, 0) <= tuple(map(int, match.groups())) < (8, 12, 2):
            affected.append((path, version))
    return affected


class PiDependencySecurityAcceptance(unittest.TestCase):
    def test_every_locked_copy_is_outside_each_reported_advisory_range(self):
        lock = json.loads((ROOT / "infra/image/pi/package-lock.json").read_text())
        packages = lock["packages"]
        self.assertTrue(undici_copies(packages), "dependency inventory changed: review the HTTP-client gate")
        for advisory in ADVISORIES:
            with self.subTest(advisory=advisory):
                self.assertEqual(vulnerable_copies(packages), [],
                                 f"{advisory}: installed undici must be outside >=8.0.0,<8.12.2")

    def test_patched_top_level_copy_cannot_hide_vulnerable_nested_or_alias_copy(self):
        for path, metadata in (
            ("node_modules/parent/node_modules/undici", {}),
            ("node_modules/@scope/parent/node_modules/undici", {}),
            ("node_modules/http-client", {"name": "undici"}),
        ):
            with self.subTest(path=path):
                packages = {"node_modules/undici": {"version": "8.12.3"},
                            path: dict(metadata, version="8.10.2")}
                self.assertEqual(vulnerable_copies(packages), [(path, "8.10.2")])

    def test_advisory_boundaries_do_not_require_one_exact_patched_version(self):
        for version, affected in (("7.99.0", False), ("8.0.0", True), ("8.12.1", True),
                                  ("8.12.2", False), ("8.12.3", False), ("9.0.0", False)):
            with self.subTest(version=version):
                packages = {"node_modules/undici": {"version": version}}
                self.assertEqual(bool(vulnerable_copies(packages)), affected)

    def test_unknown_or_prerelease_versions_cannot_silently_pass_the_gate(self):
        for version in ("", "latest", "8.12.2-rc.1"):
            with self.subTest(version=version):
                with self.assertRaisesRegex(ValueError, "stable undici release"):
                    vulnerable_copies({"node_modules/undici": {"version": version}})


if __name__ == "__main__":
    unittest.main()
