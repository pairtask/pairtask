"""Exercise CI image decisions through the public CLI and real Git diffs."""

from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / "scripts"))
import sandbox_source


class ClassifierAcceptanceTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.repo = Path(temporary.name) / "repository with spaces"
        self.repo.mkdir()
        self.git("init", "-q")
        self.base = self.commit()

    def git(self, *args):
        return subprocess.run(
            ["git", *args], cwd=self.repo, check=True, capture_output=True,
        ).stdout.decode().strip()

    def commit(self):
        self.git("add", "--all")
        self.git("-c", "user.name=Acceptance", "-c", "user.email=test@example.invalid",
                 "-c", "commit.gpgsign=false", "commit", "--allow-empty", "-qm", "Fixture")
        return self.git("rev-parse", "HEAD")

    def write(self, name):
        target = self.repo / name
        target.parent.mkdir(parents=True, exist_ok=True)
        target.write_text("fixture content\n")

    def run_cli(self, *args):
        return subprocess.run(
            [sys.executable, str(ROOT / "scripts/ci-classify.py"),
             "--repo", str(self.repo), *args],
            capture_output=True, text=True, timeout=15,
        )

    def decision(self, base, head):
        result = self.run_cli("--event", "pull_request", "--base=" + base, "--head=" + head)
        self.assertEqual(result.returncode, 0, result.stderr)
        return result.stdout

    def assert_builds(self, base, head):
        self.assertEqual(self.decision(base, head),
                         "images=true\nreason=shipped-or-unknown-path\n")

    def test_only_readme_and_top_level_markdown_can_skip(self):
        for name in ("README.md", "docs/operator-chat.md", "docs/new guide.md"):
            self.write(name)
        self.assertEqual(self.decision(self.base, self.commit()),
                         "images=false\nreason=safe-docs-only\n")

    def test_unknown_or_shipped_paths_build_even_with_safe_docs(self):
        self.write("README.md")
        for name in ("internal/example.go", "scripts/tool.py", "infra/image/install.sh",
                     ".github/workflows/ci.yml", "go.mod", "LICENSE", "readme.md",
                     "docs/nested/guide.md", "docs/diagram.svg", "docs/guide.MD",
                     "docs/skills/collab-ai/SKILL.md"):
            with self.subTest(path=name):
                self.write(name)
                self.assert_builds(self.base, self.commit())
                (self.repo / name).unlink()

    def test_all_shipped_markdown_still_requires_images(self):
        # Derive these inputs from the actual archive, so a new shipped doc
        # cannot silently become eligible for the documentation shortcut.
        paths = [path for path in sandbox_source.RECIPE_PATHS if path.endswith(".md")]
        paths += [path + "/README.md" for path in sandbox_source.APP_PATHS
                  if (ROOT / path).is_dir()]
        for name in paths:
            with self.subTest(path=name):
                self.write(name)
                self.assert_builds(self.base, self.commit())
                (self.repo / name).unlink()

    def test_deletion_uses_the_deleted_path(self):
        for name in ("docs/old.md", "infra/image/old.sh"):
            self.write(name)
        before = self.commit()
        (self.repo / "docs/old.md").unlink()
        docs_deleted = self.commit()
        self.assertEqual(self.decision(before, docs_deleted),
                         "images=false\nreason=safe-docs-only\n")
        (self.repo / "infra/image/old.sh").unlink()
        self.assert_builds(docs_deleted, self.commit())

    def test_rename_checks_both_old_and_new_path(self):
        self.write("infra/image/recipe.sh")
        before = self.commit()
        (self.repo / "docs").mkdir()
        (self.repo / "infra/image/recipe.sh").rename(self.repo / "docs/recipe.md")
        renamed = self.commit()
        self.assert_builds(before, renamed)
        self.assert_builds(renamed, before)

    def test_safe_doc_rename_can_skip(self):
        self.write("docs/before.md")
        before = self.commit()
        (self.repo / "docs/before.md").rename(self.repo / "docs/after.md")
        self.assertEqual(self.decision(before, self.commit()),
                         "images=false\nreason=safe-docs-only\n")

    def test_whitespace_in_paths_is_not_a_diff_delimiter(self):
        for name in ("docs/space name.md", "docs/tab\tname.md", "docs/line\nname.md"):
            self.write(name)
        docs = self.commit()
        self.assertEqual(self.decision(self.base, docs),
                         "images=false\nreason=safe-docs-only\n")
        self.write("unknown\nREADME.md")
        self.assert_builds(docs, self.commit())

    def test_full_diff_includes_changes_before_the_last_commit(self):
        self.write("cmd/main.go")
        self.commit()
        self.write("README.md")
        self.assert_builds(self.base, self.commit())

    def test_valid_empty_diff_can_skip(self):
        self.assertEqual(self.decision(self.base, self.base),
                         "images=false\nreason=safe-docs-only\n")

    def test_invalid_or_missing_refs_fail_closed(self):
        for base, head in (("missing-base", self.base), (self.base, "missing-head"),
                           ("", self.base), (self.base, ""), ("--stat", self.base)):
            with self.subTest(base=base, head=head):
                self.assertEqual(self.decision(base, head),
                                 "images=true\nreason=diff-failed\n")

    def test_non_repository_diff_fails_closed(self):
        self.repo = self.repo.parent
        self.assertEqual(self.decision(self.base, self.base),
                         "images=true\nreason=diff-failed\n")

    def test_every_non_pr_event_builds_without_valid_refs(self):
        for event in ("push", "workflow_dispatch", "schedule", "pull_request_target",
                      "release", "unknown", ""):
            with self.subTest(event=event):
                result = self.run_cli("--event", event, "--base", "missing", "--head", "missing")
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertEqual(result.stdout, "images=true\nreason=event-always-builds\n")

    def test_usage_error_cannot_report_a_safe_skip(self):
        result = self.run_cli("--unexpected-option")
        self.assertNotEqual(result.returncode, 0)
        self.assertNotIn("images=false", result.stdout)
