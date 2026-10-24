"""Check portable links and provenance in the bundle installed by the image recipe."""

import hashlib
import json
from pathlib import Path
import re
import subprocess
import sys
import tarfile
import tempfile
import unittest
from urllib.parse import unquote, urlsplit

ROOT = Path(__file__).resolve().parents[2]
SKILL = ROOT / "docs/skills/collab-ai"
sys.path.insert(0, str(ROOT / "scripts"))
import sandbox_source


def markdown_files(directory):
    return {path.relative_to(directory): path.read_bytes() for path in directory.rglob("*.md")}


def local_links(contents):
    for target in re.findall(r"\[[^\]]+\]\(([^)]+)\)", contents):
        link = urlsplit(target)
        if not link.scheme and link.path:
            yield unquote(link.path)


def reachable_documents(directory):
    pending = {Path("SKILL.md")}
    reached = set()
    while pending:
        relative = pending.pop()
        reached.add(relative)
        document = directory / relative
        links = {(document.parent / target).resolve().relative_to(directory.resolve())
                 for target in local_links(document.read_text()) if target.endswith(".md")}
        pending.update(links - reached)
    return reached


class SkillBundleTests(unittest.TestCase):
    def test_image_installer_preserves_bundle_and_resolvable_links(self):
        with tempfile.TemporaryDirectory() as temporary:
            installed = Path(temporary) / "installed"
            subprocess.run(["sh", str(ROOT / "infra/image/install-skill.sh"),
                            str(SKILL), str(installed)], check=True)
            self.assertEqual(markdown_files(installed), markdown_files(SKILL))
            for relative, contents in markdown_files(installed).items():
                self.check_local_links(installed, relative, contents.decode())
            self.assertEqual(reachable_documents(installed), set(markdown_files(installed)),
                             "Every installed reference must be discoverable from SKILL.md")

    def check_local_links(self, installed, relative, contents):
        for target in local_links(contents):
            resolved = (installed / relative).parent / target
            with self.subTest(document=str(relative), target=target):
                self.assertTrue(resolved.resolve().is_relative_to(installed.resolve()))
                self.assertTrue(resolved.is_file(), f"Missing bundled reference: {target}")

    def test_source_archive_contains_and_hashes_every_skill_document(self):
        with tempfile.TemporaryDirectory() as temporary:
            output = Path(temporary) / "source.tar"
            sandbox_source.source_archive(output)
            with tarfile.open(output) as archive:
                manifest = json.load(archive.extractfile("build-source.json"))
                for relative, contents in markdown_files(SKILL).items():
                    name = "docs/skills/collab-ai/" + relative.as_posix()
                    with self.subTest(document=name):
                        self.assertEqual(archive.extractfile(name).read(), contents)
                        self.assertEqual(manifest["files_sha256"][name], hashlib.sha256(contents).hexdigest())
