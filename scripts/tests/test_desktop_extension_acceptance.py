"""The Desktop extension a user installs contains the promised server and settings."""

import json
from pathlib import Path
import struct
import subprocess
import sys
import tempfile
import unittest
import zipfile

from scripts.desktop_extension import Target, build

ROOT = Path(__file__).resolve().parents[2]
BUILDER = ROOT / "scripts" / "build-mcpb.py"
TOOLS = {"whoami", "pair_redeem", "pair_status", "send", "check_inbox", "mark_read"}
PLATFORMS = (("linux", "amd64"), ("linux", "arm64"), ("darwin", "amd64"), ("darwin", "arm64"))
ICONS = (
    ("light", "icons/pairtask-light-512.png"),
    ("dark", "icons/pairtask-inverted-512.png"),
)


def executable_header(goos, goarch):
    """A minimal executable header lets the packager verify the target, offline."""
    data = bytearray(24)
    if goos == "linux":
        data[:4] = b"\x7fELF"
        struct.pack_into("<H", data, 18, {"amd64": 0x3E, "arm64": 0xB7}[goarch])
    else:
        data[:4] = b"\xcf\xfa\xed\xfe"
        struct.pack_into("<I", data, 4, {"amd64": 0x01000007, "arm64": 0x0100000C}[goarch])
    return bytes(data)


class DesktopExtensionAcceptance(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory(prefix="desktop-extension-")
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)

    def build(self, binary, target=("linux", "amd64", "1.2.3"), out=None):
        goos, goarch, version = target
        out = out or self.root / "out"
        return subprocess.run(
            [sys.executable, str(BUILDER), "--binary", str(binary), "--goos", goos,
             "--goarch", goarch, "--version", version, "--out", str(out)],
            text=True, capture_output=True, check=False,
        )

    def test_each_platform_bundle_has_an_executable_and_installable_manifest(self):
        for goos, goarch in PLATFORMS:
            with self.subTest(goos=goos, goarch=goarch):
                binary = self.root / f"server-{goos}-{goarch}"
                payload = executable_header(goos, goarch)
                binary.write_bytes(payload)
                out = self.root / f"out-{goos}-{goarch}"
                result = self.build(binary, (goos, goarch, "1.2.3"), out)
                self.assertEqual(result.returncode, 0, result.stderr)
                bundle = out / f"pairtask-desktop-{goos}-{goarch}.mcpb"
                self.assertEqual(result.stdout.strip(), str(bundle))
                with zipfile.ZipFile(bundle) as archive:
                    self.assertEqual(
                        set(archive.namelist()),
                        {"manifest.json", "server/pairtask-desktop"} | {path for _, path in ICONS},
                    )
                    self.assertEqual(archive.read("server/pairtask-desktop"), payload)
                    self.assertEqual(archive.getinfo("server/pairtask-desktop").external_attr >> 16, 0o100755)
                    self.assertEqual(archive.getinfo("manifest.json").external_attr >> 16, 0o100644)
                    for _, path in ICONS:
                        icon = archive.read(path)
                        self.assertEqual(icon, (ROOT / "desktop" / "extension" / path).read_bytes())
                        self.assertEqual(icon[:8], b"\x89PNG\r\n\x1a\n")
                        self.assertEqual(icon[12:16], b"IHDR")
                        self.assertEqual(struct.unpack(">II", icon[16:24]), (512, 512))
                        self.assertEqual(icon[25], 6, "icon should have RGBA color")
                        self.assertEqual(archive.getinfo(path).external_attr >> 16, 0o100644)
                    manifest = json.loads(archive.read("manifest.json"))
                self.assertEqual(manifest["manifest_version"], "0.3")
                self.assertEqual(manifest["version"], "1.2.3")
                self.assertEqual(manifest["compatibility"]["platforms"], [goos])
                for field in ("name", "description", "author", "server"):
                    self.assertIn(field, manifest)
                self.assertTrue(manifest["author"]["name"])
                self.assertEqual(manifest["license"], "MIT")
                self.assertEqual(manifest["repository"]["url"], "https://github.com/pairtask/pairtask")
                self.assertEqual(manifest["icon"], ICONS[0][1])
                self.assertEqual(
                    manifest["icons"],
                    [{"src": path, "size": "512x512", "theme": theme} for theme, path in ICONS],
                )
                server = manifest["server"]
                self.assertEqual(server["type"], "binary")
                self.assertEqual(server["entry_point"], "server/pairtask-desktop")
                self.assertEqual(server["mcp_config"]["command"], "${__dirname}/server/pairtask-desktop")
                self.assertEqual(server["mcp_config"]["args"], ["--name", "${user_config.name}"])
                self.assertEqual(manifest["user_config"]["name"]["type"], "string")
                self.assertTrue(manifest["user_config"]["name"]["required"])
                self.assertEqual({tool["name"] for tool in manifest["tools"]}, TOOLS)
                first = bundle.read_bytes()
                self.assertEqual(self.build(binary, (goos, goarch, "1.2.3"), out).returncode, 0)
                self.assertEqual(bundle.read_bytes(), first, "same inputs changed the bundle")

    def test_refusals_leave_no_bundle_or_partial_file(self):
        linux = self.root / "linux-amd64"
        linux.write_bytes(executable_header("linux", "amd64"))
        cases = (
            ("missing binary", self.root / "missing", "linux", "amd64", "1.2.3"),
            ("wrong OS", linux, "darwin", "amd64", "1.2.3"),
            ("wrong architecture", linux, "linux", "arm64", "1.2.3"),
            ("bad version", linux, "linux", "amd64", "1.2"),
            ("unknown OS", linux, "windows", "amd64", "1.2.3"),
            ("unknown architecture", linux, "linux", "386", "1.2.3"),
        )
        for label, binary, goos, goarch, version in cases:
            with self.subTest(label=label):
                out = self.root / label.replace(" ", "-")
                result = self.build(binary, (goos, goarch, version), out)
                self.assertNotEqual(result.returncode, 0)
                self.assertFalse(list(out.glob("*")), "rejected build left an artifact")

    def test_missing_icon_leaves_no_partial_bundle(self):
        binary = self.root / "linux-amd64"
        binary.write_bytes(executable_header("linux", "amd64"))
        template = self.root / "manifest.json"
        manifest = json.loads((ROOT / "desktop" / "extension" / "manifest.json").read_text())
        manifest["icon"] = "icons/missing.png"
        manifest["icons"] = []
        template.write_text(json.dumps(manifest))
        out = self.root / "missing-icon-out"
        with self.assertRaises(FileNotFoundError):
            build(binary, Target("linux", "amd64", "1.2.3"), out, template)
        self.assertFalse(list(out.glob("*")), "missing icon left a partial bundle")


if __name__ == "__main__":
    unittest.main()
