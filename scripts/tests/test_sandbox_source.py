from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / "scripts"))
import sandbox_source as source


class SourceTests(unittest.TestCase):
    def test_selected_commit_is_independent_of_dirty_checkout(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            subprocess.run(["git", "init", "-q", str(root)], check=True)
            for name in ("cmd", "internal"):
                (root / name).mkdir()
                (root / name / "example.go").write_text("package example\n")
            (root / "go.mod").write_text("module original\n")
            (root / "go.sum").write_text("")
            source.git("add", ".", root=root)
            source.git("-c", "user.name=Test", "-c", "user.email=test@example.invalid",
                       "commit", "-qm", "Initial", root=root)
            revision = source.commit("HEAD", root)
            (root / "go.mod").write_text("module modified\n")
            (root / "cmd/untracked-secret").write_text("do not package")
            files, metadata = source.app_source(root, revision)
            self.assertEqual(files["go.mod"], b"module original\n")
            self.assertFalse(metadata["working_tree"])
            files, metadata = source.app_source(root, "working-tree")
            self.assertEqual(files["go.mod"], b"module modified\n")
            self.assertTrue(metadata["dirty"])
            self.assertNotIn("cmd/untracked-secret", files)

    def test_local_symlink_cannot_import_host_files(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "source.go").symlink_to("/etc/passwd")
            with self.assertRaisesRegex(ValueError, "symlink"):
                source.repository_file(root, "source.go")


class SourceArchivePathsAcceptance(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        subprocess.run(["git", "init", "-q", str(self.root)], check=True)
        self.required = {
            "go.mod": b"module example\n", "go.sum": b"",
            "cmd/example.go": b"package main\n", "internal/example.go": b"package example\n",
        }
        for name, data in {**self.required, "docs/outside.txt": b"not app source\n"}.items():
            path = self.root / name
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_bytes(data)
        self.old_revision = self.commit("Valid source before shared fixtures")

    def commit(self, message):
        source.git("add", "-A", root=self.root)
        source.git("-c", "user.name=Test", "-c", "user.email=test@example.invalid",
                   "commit", "-qm", message, root=self.root)
        return source.commit("HEAD", self.root)

    def test_current_shared_testdata_is_app_source_in_commit_and_working_tree(self):
        fixture = self.root / "testdata/names.json"
        fixture.parent.mkdir()
        fixture.write_bytes(b'["committed"]\n')
        revision = self.commit("Add shared fixtures")
        fixture.write_bytes(b'["working tree"]\n')
        (fixture.parent / "untracked.txt").write_bytes(b"not tracked\n")
        for ref, expected in ((revision, b'["committed"]\n'), ("working-tree", b'["working tree"]\n')):
            with self.subTest(ref=ref):
                files, _ = source.app_source(self.root, ref)
                self.assertEqual(files, {**self.required, "testdata/names.json": expected})

    def test_historical_valid_app_without_optional_testdata_still_archives(self):
        fixture = self.root / "testdata/names.json"
        fixture.parent.mkdir()
        fixture.write_bytes(b"[]\n")
        self.commit("Add newer fixtures")
        files, metadata = source.app_source(self.root, self.old_revision)
        self.assertEqual(files, self.required)
        self.assertEqual(metadata["commit"], self.old_revision)

    def test_missing_required_app_paths_fail_instead_of_archiving_partial_or_whole_repo(self):
        paths = ("go.mod", "go.sum", "cmd", "internal")
        for missing in [(path,) for path in paths] + [paths]:
            with self.subTest(missing=missing):
                source.git("checkout", "-q", self.old_revision, root=self.root)
                source.git("rm", "-qr", "--", *missing, root=self.root)
                revision = self.commit("Remove required app paths")
                with self.assertRaises((ValueError, subprocess.CalledProcessError)):
                    source.app_source(self.root, revision)
