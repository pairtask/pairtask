"""Check portable links and provenance in the bundle installed by the image recipe."""

import hashlib
from itertools import product
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
SKILL = ROOT / "docs/skills/pairtask"
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


def foreign_legacy_entry(root, skills_name, kind):
    legacy = root / skills_name / "collab-ai"
    legacy.parent.mkdir()
    target = legacy if kind == "directory" else root / "foreign-target"
    if kind != "dangling":
        target.mkdir()
        (target / "keep").write_text("user-owned skill")
    if kind != "directory":
        legacy.symlink_to(target, target_is_directory=True)
    return legacy, target


def owned_legacy_links(root):
    for name in ("codex-skills", "claude-skills"):
        skills = root / name
        skills.mkdir()
        (skills / "collab-ai").symlink_to(root / "installed", target_is_directory=True)


def conflicting_skill(root, skills_name, kind):
    conflict = root / skills_name / "pairtask"
    target = conflict if kind == "directory" else root / "foreign-skill"
    target.mkdir()
    (target / "keep").write_text("user-owned skill")
    if kind == "symlink":
        conflict.symlink_to(target, target_is_directory=True)
    return target


class SkillBundleTests(unittest.TestCase):
    def install(self, root):
        return subprocess.run(
            ["sh", str(ROOT / "infra/image/install-skill.sh"), str(SKILL),
             str(root / "installed"), str(root / "codex-skills"), str(root / "claude-skills")],
            capture_output=True, text=True, check=False)

    def test_upgrade_removes_only_owned_legacy_links_and_is_idempotent(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            installed = root / "installed"
            for name in ("codex-skills", "claude-skills"):
                skills = root / name
                skills.mkdir()
                (skills / "collab-ai").symlink_to(installed, target_is_directory=True)
            for _ in range(2):
                result = self.install(root)
                self.assertEqual(result.returncode, 0, result.stderr)
                for name in ("codex-skills", "claude-skills"):
                    skills = root / name
                    self.assertEqual([entry.name for entry in skills.iterdir()], ["pairtask"])
                    self.assertEqual((skills / "pairtask").resolve(), installed.resolve())

    def test_upgrade_preserves_foreign_legacy_entries(self):
        for skills_name, kind in product(("codex-skills", "claude-skills"), ("directory", "symlink", "dangling")):
            with self.subTest(skills=skills_name, kind=kind), tempfile.TemporaryDirectory() as temporary:
                root = Path(temporary)
                legacy, target = foreign_legacy_entry(root, skills_name, kind)
                result = self.install(root)
                self.assertEqual(result.returncode, 0, result.stderr)
                self.check_foreign_legacy_entry(legacy, target, kind)
                self.assertEqual((legacy.parent / "pairtask").resolve(), (root / "installed").resolve())

    def check_foreign_legacy_entry(self, legacy, target, kind):
        self.assertEqual(legacy.is_symlink(), kind != "directory")
        if legacy.is_symlink():
            self.assertEqual(legacy.readlink(), target)
        if kind != "dangling":
            self.assertEqual((legacy / "keep").read_text(), "user-owned skill")
        self.assertEqual(legacy.exists(), kind != "dangling")

    def test_upgrade_conflict_preserves_legacy_links_in_both_harnesses(self):
        for skills_name, kind in product(("codex-skills", "claude-skills"), ("directory", "symlink")):
            with self.subTest(skills=skills_name, kind=kind), tempfile.TemporaryDirectory() as temporary:
                root = Path(temporary)
                owned_legacy_links(root)
                target = conflicting_skill(root, skills_name, kind)
                result = self.install(root)
                self.assertNotEqual(result.returncode, 0)
                self.assertIn("refusing to replace", result.stderr)
                self.check_legacy_links_survive_conflict(root, skills_name)
                self.assertEqual((target / "keep").read_text(), "user-owned skill")

    def check_legacy_links_survive_conflict(self, root, skills_name):
        for name in ("codex-skills", "claude-skills"):
            skills = root / name
            self.assertEqual((skills / "collab-ai").readlink(), root / "installed")
            if name != skills_name:
                self.assertFalse((skills / "pairtask").exists())
                self.assertFalse((skills / "pairtask").is_symlink())

    def test_image_installer_preserves_bundle_and_resolvable_links(self):
        with tempfile.TemporaryDirectory() as temporary:
            installed = Path(temporary) / "installed"
            codex_skills = Path(temporary) / "codex-skills"
            claude_skills = Path(temporary) / "claude-skills"
            subprocess.run(["sh", str(ROOT / "infra/image/install-skill.sh"),
                            str(SKILL), str(installed), str(codex_skills),
                            str(claude_skills)], check=True)
            subprocess.run(["sh", str(ROOT / "infra/image/install-skill.sh"),
                            str(SKILL), str(installed), str(codex_skills),
                            str(claude_skills)], check=True)
            self.assertFalse((installed / "pairtask").exists())
            self.assertEqual(markdown_files(installed), markdown_files(SKILL))
            for skills in (codex_skills, claude_skills):
                discovered = skills / "pairtask"
                self.assertTrue(discovered.is_symlink())
                self.assertEqual(discovered.resolve(), installed.resolve())
                self.assertEqual(markdown_files(discovered), markdown_files(SKILL))
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

    def test_image_installer_does_not_replace_existing_skill(self):
        for skills_name, kind in product(("codex-skills", "claude-skills"), ("directory", "symlink")):
            with self.subTest(skills=skills_name, kind=kind), tempfile.TemporaryDirectory() as temporary:
                root = Path(temporary)
                existing = root / skills_name / "pairtask"
                existing.parent.mkdir()
                if kind == "directory":
                    existing.mkdir()
                    target = existing
                else:
                    target = root / "foreign-skill"
                    target.mkdir()
                    existing.symlink_to(target, target_is_directory=True)
                marker = target / "keep"
                marker.write_text("user skill")
                result = subprocess.run(
                    ["sh", str(ROOT / "infra/image/install-skill.sh"), str(SKILL),
                     str(root / "installed"), str(root / "codex-skills"),
                     str(root / "claude-skills")], capture_output=True, text=True,
                    check=False)
                self.assertNotEqual(result.returncode, 0)
                self.assertIn("refusing to replace", result.stderr)
                self.assertEqual(marker.read_text(), "user skill")
                self.assertEqual(list(target.iterdir()), [marker])

    def test_source_archive_contains_and_hashes_every_skill_document(self):
        with tempfile.TemporaryDirectory() as temporary:
            output = Path(temporary) / "source.tar"
            sandbox_source.source_archive(output)
            with tarfile.open(output) as archive:
                manifest = json.load(archive.extractfile("build-source.json"))
                for relative, contents in markdown_files(SKILL).items():
                    name = "docs/skills/pairtask/" + relative.as_posix()
                    with self.subTest(document=name):
                        self.assertEqual(archive.extractfile(name).read(), contents)
                        self.assertEqual(manifest["files_sha256"][name], hashlib.sha256(contents).hexdigest())
