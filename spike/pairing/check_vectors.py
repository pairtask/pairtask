#!/usr/bin/env python3
"""Run pinned upstream CPace core vectors; dependency tests need an explicit run."""

import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import subprocess
import tempfile


SPIKE = Path(__file__).resolve().parent
CANDIDATE = "github.com/the-sarge/cpace"
MODULES = {
    CANDIDATE: {
        "version": "v0.1.3",
        "sum": "h1:z8e82+GWaEjsukVfUB9MK9mh1NX8PjtCpcYxloJemJc=",
        "tests": (
            "TestEmbeddedDraftVectorJSON",
            "TestEmbeddedDraftGeneratorJSON",
            "TestEmbeddedDraftInvalidVectorJSON",
            "TestRistrettoDraft21Vectors",
            "TestCoreDraft21Vectors",
            "TestScalarMultVFYDraftInvalidVectors",
        ),
    },
    "github.com/mendsec/cpace": {
        # v0.1.0 is commit 836a13c444ce916f037f280765c6b0e721cc5092.
        "version": "v0.1.0",
        "sum": "h1:wJwWxEZwTiPT9Wgp199+kgjV6ux9DY4+02rDOLNxE5A=",
        "tests": (
            "TestPrependLenAndLvCat",
            "TestCPaceRistretto255Vectors",
            "TestScalarMultVfyVectors",
        ),
    },
}
# Decoded BASE64 JSON from draft-irtf-cfrg-cpace-21, independently verified
# against the IETF archive. Only trailing CR/LF are excluded from each hash.
# See VECTOR-CHECKS.md for provenance and the limits of this comparison.
FIXTURES = {
    "draft21-ristretto255-generator.json":
        "05c8a34bd623fbdefd7fbffcd261d2420bd34363efa301d0b0dd9817f7f47c94",
    "draft21-ristretto255-sha512.json":
        "dc74177668cc2374beaf57fcb6e4c08a908238bab6b74d8edf8c86e04bc663ae",
    "draft21-ristretto255-invalid.json":
        "6288f7ff96dfb8c2d6c4d743927c5fe6ac4aecbc56da2d1f00f27104000b6dfd",
}


def package_events(output, package):
    events = [json.loads(line) for line in output.splitlines()]
    return [event for event in events if event.get("Package") == package]


def require_passes(output, package, required):
    """Go exits zero for a missing -run match or skipped test: reject both."""
    events = package_events(output, package)
    for event in events:
        if event.get("Action") == "fail":
            raise RuntimeError(f"{package}: failed {event.get('Test', 'package')}")
    passed = {event.get("Test") for event in events if event.get("Action") == "pass"}
    missing = set(required) - passed
    if missing:
        raise RuntimeError(f"{package}: required tests did not pass: {sorted(missing)}")
    if None not in passed:
        raise RuntimeError(f"{package}: package did not pass")


class VectorChecks:
    def __init__(self, go, race):
        self.go = go
        self.race = race
        self.env = {**os.environ, "GOWORK": "off", "GOTOOLCHAIN": "local", "GOFLAGS": ""}

    def run(self, args, cwd):
        result = subprocess.run(
            [self.go, *args], cwd=cwd, env=self.env, text=True, capture_output=True, check=False
        )
        if result.returncode:
            raise RuntimeError(
                f"go {' '.join(args)} exited {result.returncode}\n"
                f"{result.stdout}{result.stderr}"
            )
        return result.stdout

    def check_selection(self):
        selected = json.loads(self.run(["list", "-m", "-json", CANDIDATE], SPIKE))
        if selected.get("Replace"):
            raise RuntimeError("spike candidate replaced: review and update the vector pins")
        if selected.get("Version") != MODULES[CANDIDATE]["version"]:
            raise RuntimeError("spike candidate changed: review and update the vector pins")

    def download(self, module, temporary):
        spec = MODULES[module]
        download = json.loads(self.run([
            "mod", "download", "-json", f"{module}@{spec['version']}"
        ], temporary))
        expected = {"Path": module, "Version": spec["version"], "Sum": spec["sum"]}
        for key, want in expected.items():
            if download.get(key) != want:
                raise RuntimeError(f"{module}: downloaded {key} does not match reviewed pin")
        if download.get("Error"):
            raise RuntimeError(download["Error"])
        # Cached metadata alone would not detect edited module-cache sources.
        Path(temporary, "go.mod").write_text(
            f"module pairtask-vector-download\n\ngo 1.26.0\n\nrequire {module} {spec['version']}\n"
        )
        self.run(["mod", "verify"], temporary)
        return Path(download["Dir"])

    def test_module(self, module, directory):
        spec = MODULES[module]
        pattern = "^(" + "|".join(re.escape(name) for name in spec["tests"]) + ")$"
        command = ["test", "-mod=readonly", "-json", "-count=1", "-timeout=120s"]
        if self.race:
            command.append("-race")
        output = self.run([*command, "-run", pattern, "."], directory)
        require_passes(output, module, spec["tests"])
        for name in spec["tests"]:
            print(f"PASS {module}@{spec['version']} {name}", flush=True)

    def check_all(self):
        self.check_selection()
        print("Vector checks: " + ("with race detector" if self.race else "WITHOUT race detector"), flush=True)
        # Do not add mendsec to the spike or application's dependency graph.
        with tempfile.TemporaryDirectory(prefix="pairtask-vectors-") as temporary:
            Path(temporary, "go.mod").write_text("module pairtask-vector-download\n\ngo 1.26.0\n")
            for module in MODULES:
                directory = self.download(module, temporary)
                if module == CANDIDATE:
                    verify_fixtures(directory)
                self.test_module(module, directory)


def verify_fixtures(directory):
    for name, want in FIXTURES.items():
        data = (directory / "testdata" / name).read_bytes().rstrip(b"\r\n")
        if hashlib.sha256(data).hexdigest() != want:
            raise RuntimeError(f"{name}: differs from the official draft-21 fixture")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--go", default="go", help="Go 1.26+ executable")
    parser.add_argument("--no-race", action="store_true", help="local hosts without cgo only")
    args = parser.parse_args()
    VectorChecks(args.go, race=not args.no_race).check_all()


if __name__ == "__main__":
    main()
