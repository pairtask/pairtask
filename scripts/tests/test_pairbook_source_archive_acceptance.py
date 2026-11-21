"""The image source archive includes the public contract when the app has it."""

import hashlib
import json
import tarfile
import unittest
from unittest import mock

import test_sandbox_source as fixtures

source = fixtures.source


class PairbookSourceArchiveAcceptance(unittest.TestCase):
    def setUp(self):
        fixtures.SourceArchivePathsAcceptance.setUp(self)

    def commit(self, message):
        return fixtures.SourceArchivePathsAcceptance.commit(self, message)

    def add_pairbook(self):
        package = self.root / "pairbook"
        (package / "testdata").mkdir(parents=True)
        (package / "header.go").write_bytes(b"package pairbook\n// committed header\n")
        (package / "testdata/header.json").write_bytes(b'{"version":1}\n')
        return self.commit("Add public contract imported by app")

    def archive(self, ref):
        destination = self.root / "image-source.tar"
        # Recipe files are independent of app selection. Keep this fixture
        # focused on the real source archive and its provenance.
        with mock.patch.object(source, "RECIPE_PATHS", []):
            provenance = source.source_archive(destination, lock={}, ref=ref, root=self.root)
        with tarfile.open(destination) as archive:
            files = {member.name: archive.extractfile(member).read()
                     for member in archive if member.isfile()}
        self.assertEqual(json.loads(files["build-source.json"]), provenance)
        for name, digest in provenance["files_sha256"].items():
            self.assertEqual(hashlib.sha256(files[name]).hexdigest(), digest)
        return files, provenance["collab"]

    def test_committed_archive_includes_contract_and_its_fixtures(self):
        revision = self.add_pairbook()
        (self.root / "pairbook/header.go").write_bytes(b"package pairbook\n// dirty header\n")
        (self.root / "pairbook/untracked-secret").write_bytes(b"do not package\n")
        files, metadata = self.archive(revision)
        self.assertEqual(files.get("pairbook/header.go"), b"package pairbook\n// committed header\n")
        self.assertEqual(files.get("pairbook/testdata/header.json"), b'{"version":1}\n')
        self.assertEqual(metadata["commit"], revision)
        self.assertFalse(metadata["working_tree"])
        self.assertNotIn("pairbook/untracked-secret", files)
        self.assertNotIn("docs/outside.txt", files)

    def test_working_tree_archive_tracks_contract_edits_without_untracked_files(self):
        revision = self.add_pairbook()
        changed = b"package pairbook\n// working tree header\n"
        (self.root / "pairbook/header.go").write_bytes(changed)
        (self.root / "pairbook/untracked-secret").write_bytes(b"do not package\n")
        files, metadata = self.archive("working-tree")
        self.assertEqual(files.get("pairbook/header.go"), changed)
        self.assertEqual(files.get("pairbook/testdata/header.json"), b'{"version":1}\n')
        self.assertEqual(metadata["commit"], revision)
        self.assertTrue(metadata["working_tree"])
        self.assertTrue(metadata["dirty"])
        self.assertNotIn("pairbook/untracked-secret", files)

    def test_old_app_without_contract_still_archives_from_new_or_old_checkout(self):
        self.add_pairbook()
        # An older selected revision must not accidentally borrow pairbook
        # from the current checkout. An old checkout must also still work.
        for ref in (self.old_revision, "working-tree"):
            if ref == "working-tree":
                source.git("checkout", "-q", self.old_revision, root=self.root)
            with self.subTest(ref=ref):
                files, metadata = self.archive(ref)
                for name, data in self.required.items():
                    self.assertEqual(files[name], data)
                self.assertFalse(any(name.startswith("pairbook/") for name in files))
                self.assertEqual(metadata["commit"], self.old_revision)
