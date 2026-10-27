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


def require_passes(output, package, required):
    """Go exits zero for a missing -run match or skipped test: reject both."""
    passed = set()
    package_passed = False
    for line in output.splitlines():
        event = json.loads(line)
        if event.get("Package") != package:
            continue
        if event.get("Action") == "fail":
            raise RuntimeError(f"{package}: failed {event.get('Test', 'package')}")
        if event.get("Action") == "pass":
            if "Test" in event:
                passed.add(event["Test"])
            else:
                package_passed = True
    missing = set(required) - passed
    if missing or not package_passed:
        raise RuntimeError(
            f"{package}: required tests did not pass: {sorted(missing)}; "
            f"package passed={package_passed}"
        )


def run(go, args, cwd, env):
    result = subprocess.run(
        [go, *args], cwd=cwd, env=env, text=True, capture_output=True, check=False
    )
    if result.returncode:
        raise RuntimeError(
            f"go {' '.join(args)} exited {result.returncode}\n"
            f"{result.stdout}{result.stderr}"
        )
    return result.stdout


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--go", default="go", help="Go 1.26+ executable")
    parser.add_argument("--no-race", action="store_true", help="local hosts without cgo only")
    args = parser.parse_args()
    env = {**os.environ, "GOWORK": "off", "GOTOOLCHAIN": "local", "GOFLAGS": ""}

    selected = json.loads(run(args.go, ["list", "-m", "-json", CANDIDATE], SPIKE, env))
    if selected.get("Replace") or selected.get("Version") != MODULES[CANDIDATE]["version"]:
        raise RuntimeError("spike candidate changed: review and update the vector pins")

    print("Vector checks: " + ("WITHOUT race detector" if args.no_race else "with race detector"), flush=True)
    # Download outside the spike module so the comparison never changes its
    # go.mod/go.sum or adds mendsec to the application's dependency graph.
    with tempfile.TemporaryDirectory(prefix="pairtask-vectors-") as temporary:
        Path(temporary, "go.mod").write_text("module pairtask-vector-download\n\ngo 1.26.0\n")
        for module, spec in MODULES.items():
            download = json.loads(run(args.go, [
                "mod", "download", "-json", f"{module}@{spec['version']}"
            ], temporary, env))
            if (download.get("Path") != module or download.get("Version") != spec["version"]
                    or download.get("Sum") != spec["sum"] or download.get("Error")):
                raise RuntimeError(f"{module}: downloaded module does not match reviewed pin")
            directory = Path(download["Dir"])
            # Also check extracted source against its checksum; cached metadata
            # alone would not detect locally edited module-cache files.
            Path(temporary, "go.mod").write_text(
                f"module pairtask-vector-download\n\ngo 1.26.0\n\nrequire {module} {spec['version']}\n"
            )
            run(args.go, ["mod", "verify"], temporary, env)
            if module == CANDIDATE:
                for name, want in FIXTURES.items():
                    data = (directory / "testdata" / name).read_bytes().rstrip(b"\r\n")
                    if hashlib.sha256(data).hexdigest() != want:
                        raise RuntimeError(f"{name}: differs from the official draft-21 fixture")
            pattern = "^(" + "|".join(re.escape(name) for name in spec["tests"]) + ")$"
            command = ["test", "-mod=readonly", "-json", "-count=1", "-timeout=120s"]
            if not args.no_race:
                command.append("-race")
            output = run(args.go, [*command, "-run", pattern, "."], directory, env)
            require_passes(output, module, spec["tests"])
            for name in spec["tests"]:
                print(f"PASS {module}@{spec['version']} {name}", flush=True)


if __name__ == "__main__":
    main()
