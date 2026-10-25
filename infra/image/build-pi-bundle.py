"""Build the Pi bundle inside the disposable bundle container (#155).

The bundle is the pinned closure of Pi and the pairtask extension's npm
dependencies, plus the extension itself, so one digest covers all of it. It
ships no examples (Pi's include a GPL DOOM build), keeps every package's
license and notice texts, and fails closed on any license outside the
allowlist. Node is downloaded from the lock only to run npm here; it ships as
its own artifact. package() works on any node_modules tree without npm, so
tests exercise the same code the image build runs.
"""

import json
import os
from pathlib import Path
import platform
import re
import shutil
import subprocess
import tempfile

ROOT = Path("/root/bundle-src")
BUNDLE = Path("/root/pi-bundle")
AGENT = "@earendil-works/pi-coding-agent"
MCP = "@earendil-works/pi-mcp"
NODE_MIN = "22.19.0"
ALLOWED = {"MIT", "ISC", "Apache-2.0", "BSD-2-Clause", "BSD-3-Clause", "0BSD", "BlueOak-1.0.0",
           "Unlicense", "CC0-1.0", "Python-2.0"}
NOTICE_FILE = re.compile(r"^(LICEN[CS]E|NOTICE|COPYING)", re.IGNORECASE)


class LicenseError(ValueError):
    """Packages whose license is missing, unparseable or not allowed."""


def prune_examples(bundle):
    """Remove every examples directory under node_modules; return what went."""
    removed = sorted({p for p in Path(bundle).rglob("examples") if p.is_dir() and "node_modules" in p.parts})
    for path in removed:
        if path.exists():
            shutil.rmtree(path)
    return removed


def inventory(bundle):
    """Every retained package: name, version, license and its notice files."""
    packages = []
    for manifest in sorted(Path(bundle).rglob("node_modules/**/package.json")):
        package = package_record(manifest)
        if package:
            packages.append(package)
    return packages


def package_record(manifest):
    """A package's record, or None for a package.json that isn't a package
    root (a nested file inside a package's own tree)."""
    directory = manifest.parent
    if not is_package_root(directory):
        return None
    data = json.loads(manifest.read_text())
    files = sorted(str(f) for f in directory.iterdir() if f.is_file() and NOTICE_FILE.match(f.name))
    return {"name": data.get("name", directory.name), "version": data.get("version", ""),
            "license": license_of(data), "license_files": files}


def is_package_root(directory):
    """node_modules/<name> or node_modules/@scope/<name>."""
    parent = directory.parent
    if parent.name == "node_modules":
        return True
    return parent.name.startswith("@") and parent.parent.name == "node_modules"


def license_of(data):
    value = data.get("license")
    if isinstance(value, dict):
        value = value.get("type")
    if value is None and isinstance(data.get("licenses"), list):
        value = " OR ".join(str(item.get("type", "")) for item in data["licenses"])
    return value


def check_licenses(packages):
    """Raise naming every package whose license isn't allowed. An OR
    expression passes only when every alternative is allowed; anything else,
    AND included, fails closed."""
    offenders = [f'{p["name"]}@{p["version"]}: {p["license"]!r}' for p in packages if not allowed(p["license"])]
    if offenders:
        raise LicenseError("licenses outside the allowlist: " + "; ".join(offenders))


def allowed(expression):
    """An allowed license, or "A OR B …" of allowed licenses, optionally in
    one pair of outer parentheses. Anything else is unparseable here."""
    if not isinstance(expression, str):
        return False
    text = expression.strip()
    if text.startswith("(") and text.endswith(")"):
        text = text[1:-1]
    if "(" in text or ")" in text:
        return False
    return all(term in ALLOWED for term in text.split(" OR "))


def write_notices(bundle, packages):
    """THIRD_PARTY_NOTICES.txt: every package and its license and notice texts."""
    sections = []
    for p in packages:
        texts = [Path(f).read_text(errors="replace").strip() for f in p["license_files"]]
        sections.append(f'{p["name"]}@{p["version"]} ({p["license"]})\n\n' + "\n\n".join(texts))
    path = Path(bundle) / "THIRD_PARTY_NOTICES.txt"
    path.write_text("Third-party software in the Pi bundle\n\n" + "\n\n----\n\n".join(sections) + "\n")
    return path


def final_scan(bundle):
    """Nothing the bundle must not ship: no examples, no DOOM build anywhere."""
    leftovers = [p for p in Path(bundle).rglob("*") if p.name == "doom.wasm" or (p.is_dir() and p.name == "examples")]
    if leftovers:
        raise ValueError("the Pi bundle still ships examples or doom.wasm: " + ", ".join(map(str, leftovers)))


def cli_path(bundle):
    """Pi's command-line entry, relative to the bundle."""
    agent = Path(bundle) / "node_modules" / AGENT
    entry = json.loads((agent / "package.json").read_text())["bin"]
    entry = entry if isinstance(entry, str) else entry["pi"]
    return str((agent / entry).relative_to(bundle))


def versions(packages):
    found = {p["name"]: p["version"] for p in packages}
    return found[AGENT], found[MCP]


def package(bundle, extension):
    """Turn an installed node_modules tree into the bundle and describe it."""
    bundle = Path(bundle)
    prune_examples(bundle)
    packages = inventory(bundle)
    check_licenses(packages)
    final_scan(bundle)
    (bundle / "extension").mkdir(exist_ok=True)
    shutil.copyfile(extension, bundle / "extension/index.ts")
    write_notices(bundle, packages)
    agent, mcp = versions(packages)
    details = {"pi_coding_agent": agent, "pi_mcp": mcp, "node_min": NODE_MIN, "cli": cli_path(bundle)}
    (bundle / "bundle.json").write_text(json.dumps(details, indent=2) + "\n")
    return details


def build(lock, machine, temporary):
    from tool_download import download, extract
    from tool_lock import artifact

    node = artifact(lock, "node", machine)
    archive = temporary / "node.tar.gz"
    download(node["url"], node["sha256"], archive)
    extract(archive, temporary)
    binary = temporary / node["entry"]
    BUNDLE.mkdir()
    for name in ("package.json", "package-lock.json"):
        shutil.copyfile(ROOT / "pi" / name, BUNDLE / name)
    env = dict(os.environ, PATH=f"{binary.parent}:{os.environ['PATH']}")
    subprocess.run([binary.parent / "npm", "ci", "--ignore-scripts", "--no-audit", "--no-fund"],
                   cwd=BUNDLE, env=env, check=True)
    return package(BUNDLE, ROOT / "pi-extension-source.ts")


if __name__ == "__main__":
    lock = json.loads((ROOT / "tools.lock.json").read_text())
    with tempfile.TemporaryDirectory() as directory:
        build(lock, platform.machine(), Path(directory))
