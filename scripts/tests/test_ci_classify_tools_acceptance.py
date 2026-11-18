"""The tools lane: a pull request that only re-pins agent clients in the tool
lock skips the image builds; any other lock change runs everything."""

import copy
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[2]
LOCK = "infra/image/tools.lock.json"
REAL_LOCK = json.loads((ROOT / LOCK).read_text())
LANE_TOOLS = ("claude", "codex", "cs-mcp")
LANE = "images=false\nreason=tools-lane\n"
BUILD = "images=true\nreason=shipped-or-unknown-path\n"


def repin(lock, tool, version="9.9.9"):
    """lock with tool moved to version and a new checksum per architecture."""
    entry = lock["artifacts"][tool]
    entry["version"] = version
    for index, arch in enumerate(sorted(entry["architectures"])):
        entry["architectures"][arch]["sha256"] = format(index + 1, "x") * 64
    return lock


class ToolsLaneClassifierAcceptance(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.repo = Path(temporary.name) / "repository with spaces"
        self.repo.mkdir()
        self.git("init", "-q")
        self.write_lock(REAL_LOCK)
        self.base = self.commit()

    def git(self, *args):
        return subprocess.run(["git", *args], cwd=self.repo, check=True,
                              capture_output=True).stdout.decode().strip()

    def commit(self):
        self.git("add", "--all")
        self.git("-c", "user.name=Acceptance", "-c", "user.email=test@example.invalid",
                 "-c", "commit.gpgsign=false", "commit", "--allow-empty", "-qm", "Fixture")
        return self.git("rev-parse", "HEAD")

    def write(self, name, text="fixture content\n"):
        target = self.repo / name
        target.parent.mkdir(parents=True, exist_ok=True)
        target.write_text(text)

    def write_lock(self, lock):
        self.write(LOCK, json.dumps(lock, indent=2) + "\n")

    def changed_lock(self, change):
        lock = copy.deepcopy(REAL_LOCK)
        change(lock)
        self.write_lock(lock)
        return self.commit()

    def classify(self, head, *args, event="pull_request", base=None):
        result = subprocess.run(
            [sys.executable, str(ROOT / "scripts/ci-classify.py"), "--repo", str(self.repo),
             "--event", event, "--base=" + (base or self.base), "--head=" + head, *args],
            capture_output=True, text=True, timeout=15)
        self.assertEqual(result.returncode, 0, result.stderr)
        lines = result.stdout.splitlines(keepends=True)
        self.assertEqual(len(lines), 8, result.stdout)
        flags = dict(line.rstrip("\n").split("=", 1) for line in lines[3:])
        return "".join(lines[:2]), lines[2], flags

    def assert_lane(self, head, scripts="true"):
        decision, docs_only, flags = self.classify(head)
        self.assertEqual(decision, LANE)
        self.assertEqual(docs_only, "docs_only=false\n")
        self.assertEqual(flags, {"go": "false", "scripts": scripts, "pi": "false",
                                 "infra": "false", "tools": "true"})

    def assert_full_without_lane(self, head):
        decision, _, flags = self.classify(head)
        self.assertEqual(decision, BUILD)
        self.assertEqual(flags["tools"], "false")
        self.assertEqual(flags["infra"], "true")

    def test_each_agent_client_repin_takes_the_lane(self):
        for tool in LANE_TOOLS:
            with self.subTest(tool=tool):
                self.git("reset", "--hard", self.base)
                self.assert_lane(self.changed_lock(lambda lock: repin(lock, tool)))

    def test_all_agent_clients_repinned_together_take_the_lane(self):
        def all_clients(lock):
            for tool in LANE_TOOLS:
                repin(lock, tool)
        self.assert_lane(self.changed_lock(all_clients))

    def test_only_the_version_or_only_a_checksum_also_qualifies(self):
        def version_only(lock):
            lock["artifacts"]["claude"]["version"] = "9.9.9"

        def one_checksum(lock):
            lock["artifacts"]["codex"]["architectures"]["x86_64"]["sha256"] = "e" * 64
        for name, change in (("version", version_only), ("checksum", one_checksum)):
            with self.subTest(change=name):
                self.git("reset", "--hard", self.base)
                self.assert_lane(self.changed_lock(change))

    def test_repin_with_script_tests_and_safe_docs_stays_in_the_lane(self):
        self.write("scripts/tests/test_claude_launcher_acceptance.py")
        self.write("docs/quickstart.md")
        self.assert_lane(self.changed_lock(lambda lock: repin(lock, "claude")))

    def test_every_other_lock_change_runs_everything(self):
        def field(tool, key, value):
            def change(lock):
                lock["artifacts"][tool][key] = value
            return change

        def arch_label(lock):
            lock["artifacts"]["codex"]["architectures"]["aarch64"]["arch"] = "other"

        def base_image(lock):
            lock["architectures"]["aarch64"]["base_image"] = "f" * 64

        def schema(lock):
            lock["schema"] = lock["schema"] + 1

        def new_tool(lock):
            lock["artifacts"]["gh"] = copy.deepcopy(lock["artifacts"]["rtk"])

        def dropped_tool(lock):
            del lock["artifacts"]["cs-mcp"]

        def new_architecture(lock):
            lock["artifacts"]["claude"]["architectures"]["riscv64"] = {
                "arch": "linux-riscv64", "sha256": "d" * 64}

        def extra_field(lock):
            lock["artifacts"]["claude"]["mirror"] = "https://example.invalid/"
        cases = {
            "go version": lambda lock: repin(lock, "go"),
            "node checksum": lambda lock: repin(lock, "node"),
            "rtk version": lambda lock: repin(lock, "rtk"),
            "starship version": lambda lock: repin(lock, "starship"),
            "client url": field("claude", "url", "https://example.invalid/{version}"),
            "client entry": field("codex", "entry", "bin/other"),
            "client kind": field("cs-mcp", "kind", "archive"),
            "architecture label": arch_label,
            "base image": base_image,
            "schema": schema,
            "added tool": new_tool,
            "removed tool": dropped_tool,
            "added architecture": new_architecture,
            "added field": extra_field,
        }
        for name, change in cases.items():
            with self.subTest(change=name):
                self.git("reset", "--hard", self.base)
                self.assert_full_without_lane(self.changed_lock(change))

    def test_a_client_repin_mixed_with_another_lock_change_runs_everything(self):
        def mixed(lock):
            repin(lock, "claude")
            repin(lock, "go")
        self.assert_full_without_lane(self.changed_lock(mixed))

    def test_an_unreadable_lock_on_either_side_runs_everything(self):
        self.write(LOCK, "{not json\n")
        broken = self.commit()
        with self.subTest(side="head"):
            self.assert_full_without_lane(broken)
        self.write_lock(repin(copy.deepcopy(REAL_LOCK), "claude"))
        repinned = self.commit()
        with self.subTest(side="base"):
            decision, _, flags = self.classify(repinned, base=broken)
            self.assertEqual(decision, BUILD)
            self.assertEqual(flags["tools"], "false")

    def test_an_added_deleted_or_renamed_lock_runs_everything(self):
        self.git("rm", "-q", LOCK)
        deleted = self.commit()
        with self.subTest(change="deleted"):
            self.assert_full_without_lane(deleted)
        self.write_lock(repin(copy.deepcopy(REAL_LOCK), "claude"))
        added = self.commit()
        with self.subTest(change="added"):
            decision, _, flags = self.classify(added, base=deleted)
            self.assertEqual(decision, BUILD)
            self.assertEqual(flags["tools"], "false")
        self.git("reset", "--hard", self.base)
        self.git("mv", LOCK, "infra/image/tools.lock.old.json")
        with self.subTest(change="renamed"):
            self.assert_full_without_lane(self.commit())

    def test_a_repin_with_code_runs_everything_and_the_lane(self):
        self.write("cmd/broker/main.go")
        head = self.changed_lock(lambda lock: repin(lock, "codex"))
        decision, _, flags = self.classify(head)
        self.assertEqual(decision, BUILD)
        self.assertEqual(flags, {"go": "true", "scripts": "true", "pi": "false",
                                 "infra": "false", "tools": "true"})

    def test_a_repin_with_a_ci_or_classifier_change_runs_every_job(self):
        # A script test that is also a CI path must not ride the lane's
        # scripts/tests allowance: the classifier itself changed.
        for path in ("scripts/tests/test_ci_classify_acceptance.py",
                     "scripts/tests/test_ci_classify_tools_acceptance.py",
                     "scripts/ci-classify.py", ".github/workflows/ci.yml"):
            with self.subTest(path=path):
                self.git("reset", "--hard", self.base)
                self.write(path)
                head = self.changed_lock(lambda lock: repin(lock, "claude"))
                decision, _, flags = self.classify(head)
                self.assertEqual(decision, BUILD)
                self.assertEqual(flags, {"go": "true", "scripts": "true", "pi": "true",
                                         "infra": "true", "tools": "true"})

    def test_force_full_builds_but_the_lane_still_follows_the_diff(self):
        head = self.changed_lock(lambda lock: repin(lock, "claude"))
        decision, _, flags = self.classify(head, "--force-full")
        self.assertEqual(decision, "images=true\nreason=forced-full\n")
        self.assertEqual(flags["tools"], "true")

    def test_an_unreadable_diff_runs_everything_without_the_lane(self):
        head = self.changed_lock(lambda lock: repin(lock, "claude"))
        decision, _, flags = self.classify(head, base="missing-base")
        self.assertEqual(decision, "images=true\nreason=diff-failed\n")
        self.assertEqual(flags["tools"], "false")

    def test_a_main_push_with_a_repin_builds_and_proves_images(self):
        head = self.changed_lock(lambda lock: repin(lock, "claude"))
        decision, docs_only, flags = self.classify(head, "--ref", "refs/heads/main", event="push")
        self.assertEqual(decision, "images=true\nreason=event-always-builds\n")
        self.assertEqual(docs_only, "docs_only=false\n")
        self.assertEqual(flags, {"go": "true", "scripts": "true", "pi": "true",
                                 "infra": "true", "tools": "false"})


if __name__ == "__main__":
    unittest.main()
