"""Build pairtask-desktop's Claude Desktop extension: an MCPB bundle.

An .mcpb is a zip with manifest.json at its root (MCPB manifest 0.3) and
the server it names; this one carries the pairtask-desktop binary for one
operating system and architecture, since a manifest names platforms only by
operating system. The manifest is desktop/extension/manifest.json with this
build's version and platform filled in. The bundle is reproducible: entries
in a fixed order with a fixed timestamp, the binary executable.

A binary for another platform than the one named is refused, read from its
own header, so a bundle never claims a platform its binary can't run on.
"""

import json
from pathlib import Path
import re
import struct
import zipfile

MANIFEST = Path(__file__).resolve().parent.parent / "desktop" / "extension" / "manifest.json"
ENTRY = "server/pairtask-desktop"

# Go's GOOS and GOARCH, and how each is told apart in a binary's header.
ELF, MACHO = b"\x7fELF", b"\xcf\xfa\xed\xfe"
FORMATS = {"linux": ELF, "darwin": MACHO}
MACHINES = {
    (ELF, "amd64"): 0x3E,
    (ELF, "arm64"): 0xB7,
    (MACHO, "amd64"): 0x01000007,
    (MACHO, "arm64"): 0x0100000C,
}

SEMVER = re.compile(
    r"^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)"
    r"(?:-[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?(?:\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?$"
)

# Zip's earliest time: no build's clock reaches the bundle.
EPOCH = (1980, 1, 1, 0, 0, 0)


def bundle_name(goos, goarch):
    return f"pairtask-desktop-{goos}-{goarch}.mcpb"


def build(binary, goos, goarch, version, out_dir, template=MANIFEST):
    """Write the bundle for binary into out_dir and return its path."""
    data = Path(binary).read_bytes()
    check(data, goos, goarch, version)
    manifest = manifest_for(json.loads(Path(template).read_text(encoding="utf-8")), version, goos)
    out_dir = Path(out_dir)
    out_dir.mkdir(parents=True, exist_ok=True)
    path = out_dir / bundle_name(goos, goarch)
    partial = path.with_name(path.name + ".partial")
    with zipfile.ZipFile(partial, "w") as bundle:
        add(bundle, "manifest.json", (json.dumps(manifest, indent=2) + "\n").encode("utf-8"), 0o644)
        add(bundle, ENTRY, data, 0o755)
    partial.replace(path)
    return path


def check(data, goos, goarch, version):
    """Refuse an unknown platform, a version that isn't semver, or a binary
    that isn't for goos/goarch."""
    if goos not in FORMATS:
        raise ValueError(f"unsupported GOOS {goos!r}: want one of {sorted(FORMATS)}")
    if (FORMATS[goos], goarch) not in MACHINES:
        raise ValueError(f"unsupported GOARCH {goarch!r}: want amd64 or arm64")
    if not SEMVER.match(version):
        raise ValueError(f"version {version!r} is not semver")
    if binary_platform(data) != (goos, goarch):
        raise ValueError(f"the binary is not a {goos}/{goarch} executable")


def binary_platform(data):
    """The (GOOS, GOARCH) a binary's header names, or None."""
    magic = data[:4]
    if magic == ELF and len(data) >= 20:
        machine = struct.unpack_from("<H", data, 18)[0]
    elif magic == MACHO and len(data) >= 8:
        machine = struct.unpack_from("<I", data, 4)[0]
    else:
        return None
    for (fmt, goarch), value in MACHINES.items():
        if fmt == magic and value == machine:
            return next(goos for goos, f in FORMATS.items() if f == magic), goarch
    return None


def manifest_for(template, version, goos):
    """The template with this build's version and its one platform."""
    manifest = dict(template)
    manifest["version"] = version
    compatibility = dict(manifest.get("compatibility", {}))
    compatibility["platforms"] = [goos]
    manifest["compatibility"] = compatibility
    return manifest


def add(bundle, name, data, mode):
    """Add one regular file with a fixed time and its Unix mode."""
    info = zipfile.ZipInfo(name, EPOCH)
    info.create_system = 3  # Unix, so the mode below is read on extraction
    info.external_attr = (0o100000 | mode) << 16
    info.compress_type = zipfile.ZIP_DEFLATED
    bundle.writestr(info, data)
