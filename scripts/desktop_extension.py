"""Build pairtask-desktop's Claude Desktop extension: an MCPB bundle.

An .mcpb is a zip with manifest.json at its root (MCPB manifest 0.3), the
server it names and the icons it lists; this one carries the pairtask-desktop
binary for one operating system and architecture, since a manifest names
platforms only by operating system. The manifest is desktop/extension/manifest.json with this
build's version and platform filled in. The bundle is reproducible: entries
in a fixed order with a fixed timestamp, the binary executable.

A binary for another platform than the one named is refused, read from its
own header, so a bundle never claims a platform its binary can't run on.
"""

import json
from pathlib import Path
import re
import struct
from typing import NamedTuple
import zipfile

MANIFEST = Path(__file__).resolve().parent.parent / "desktop" / "extension" / "manifest.json"
ENTRY = "server/pairtask-desktop"

# Each executable format by its magic: where its machine field sits, how it
# is read, and the GOOS it serves; then each machine value's GOARCH.
ELF, MACHO = b"\x7fELF", b"\xcf\xfa\xed\xfe"
HEADERS = {ELF: ("<H", 18, "linux"), MACHO: ("<I", 4, "darwin")}
MACHINES = {
    (ELF, 0x3E): "amd64",
    (ELF, 0xB7): "arm64",
    (MACHO, 0x01000007): "amd64",
    (MACHO, 0x0100000C): "arm64",
}
FORMATS = {goos: magic for magic, (_, _, goos) in HEADERS.items()}
PLATFORMS = {(HEADERS[magic][2], goarch) for (magic, _), goarch in MACHINES.items()}

SEMVER = re.compile(
    r"^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)"
    r"(?:-[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?(?:\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?$"
)

# Zip's earliest time: no build's clock reaches the bundle.
EPOCH = (1980, 1, 1, 0, 0, 0)


class Target(NamedTuple):
    """What a bundle is built for: Go's GOOS and GOARCH, and its version."""

    goos: str
    goarch: str
    version: str


def bundle_name(target):
    return f"pairtask-desktop-{target.goos}-{target.goarch}.mcpb"


def build(binary, target, out_dir, template=MANIFEST):
    """Write target's bundle for binary into out_dir and return its path;
    everything it carries is read first, so a missing asset fails before
    any output is created."""
    data = Path(binary).read_bytes()
    check(data, target)
    manifest = manifest_for(json.loads(Path(template).read_text(encoding="utf-8")), target)
    icons = [(name, read_asset(Path(template).parent, name)) for name in icon_files(manifest)]
    out_dir = Path(out_dir)
    out_dir.mkdir(parents=True, exist_ok=True)
    path = out_dir / bundle_name(target)
    partial = path.with_name(path.name + ".partial")
    with zipfile.ZipFile(partial, "w") as bundle:
        add(bundle, "manifest.json", (json.dumps(manifest, indent=2) + "\n").encode("utf-8"), 0o644)
        add(bundle, ENTRY, data, 0o755)
        for name, icon in icons:
            add(bundle, name, icon, 0o644)
    partial.replace(path)
    return path


def check(data, target):
    """Refuse an unknown platform, a version that isn't semver, or a binary
    that isn't for the target's platform."""
    problem = platform_problem(target) or version_problem(target) or binary_problem(data, target)
    if problem:
        raise ValueError(problem)


def platform_problem(target):
    """Why the target's platform isn't one a bundle is built for, or None."""
    if target.goos not in FORMATS:
        return f"unsupported GOOS {target.goos!r}: want one of {sorted(FORMATS)}"
    if (target.goos, target.goarch) not in PLATFORMS:
        return f"unsupported GOARCH {target.goarch!r}: want amd64 or arm64"
    return None


def version_problem(target):
    """Why the target's version isn't semver, or None."""
    if SEMVER.match(target.version):
        return None
    return f"version {target.version!r} is not semver"


def binary_problem(data, target):
    """Why the binary isn't the target's executable, or None."""
    if binary_platform(data) == (target.goos, target.goarch):
        return None
    return f"the binary is not a {target.goos}/{target.goarch} executable"


def binary_platform(data):
    """The (GOOS, GOARCH) a binary's header names, or None."""
    magic = data[:4]
    header = HEADERS.get(magic)
    if header is None:
        return None
    layout, offset, goos = header
    if len(data) < offset + struct.calcsize(layout):
        return None
    goarch = MACHINES.get((magic, struct.unpack_from(layout, data, offset)[0]))
    return (goos, goarch) if goarch else None


def manifest_for(template, target):
    """The template with the target's version and its one platform."""
    manifest = dict(template)
    manifest["version"] = target.version
    compatibility = dict(manifest.get("compatibility", {}))
    compatibility["platforms"] = [target.goos]
    manifest["compatibility"] = compatibility
    return manifest


def icon_files(manifest):
    """The manifest's icon paths, unique and sorted for reproducible bundles."""
    names = [manifest.get("icon")] + [icon.get("src") for icon in manifest.get("icons", [])]
    return sorted({name for name in names if name})


def read_asset(root, name):
    """An asset beside the manifest, refused when its path would leave that
    directory or names a URL: a bundle carries only files of its own."""
    path = (root / name).resolve()
    if "://" in name or not path.is_relative_to(root.resolve()):
        raise ValueError(f"asset {name!r} is not a file beside the manifest")
    return path.read_bytes()


def add(bundle, name, data, mode):
    """Add one regular file with a fixed time and its Unix mode."""
    info = zipfile.ZipInfo(name, EPOCH)
    info.create_system = 3  # Unix, so the mode below is read on extraction
    info.external_attr = (0o100000 | mode) << 16
    info.compress_type = zipfile.ZIP_DEFLATED
    bundle.writestr(info, data)
