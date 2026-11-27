"""publish-release.sh against a recording gh: which tag, never replacing a
published release, never deleting a tag, and latest only moving forward."""

from dataclasses import dataclass, field
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import textwrap
import unittest

SCRIPT = Path(__file__).resolve().parents[1] / "publish-release.sh"
SHA = "a" * 40

FAKE_GH = textwrap.dedent('''\
    #!PYTHON
    import json, os, sys
    state = json.load(open(os.environ["GH_STATE"]))
    with open(os.environ["GH_CALLS"], "a") as log:
        log.write(json.dumps(sys.argv[1:]) + "\\n")
    args = sys.argv[1:]
    def answer(kind, value, missing):
        if kind in state["broken"]:
            sys.stderr.write("HTTP 502: Bad Gateway\\n")
            sys.exit(1)
        if value is None:
            sys.stderr.write(missing + "\\n")
            sys.exit(1)
        print(value)
    if args[:2] == ["release", "view"]:
        release = state["releases"].get(args[2])
        answer("release", None if release is None else ("true" if release["draft"] else "false"), "release not found")
    elif args[0] == "api" and "/commits/refs/tags/" in args[1]:
        answer("tag", state["tags"].get(args[1].rsplit("/", 1)[1]), "gh: Not Found (HTTP 404)")
    elif args[0] == "api" and args[1].endswith("/releases/latest"):
        answer("latest", state.get("latest") or None, "gh: Not Found (HTTP 404)")
''')


@dataclass
class GitHub:
    """What the fake gh knows: the run, releases (tag → draft), tags (tag → commit) and latest."""
    run: int = 1165
    releases: dict = field(default_factory=dict)
    tags: dict = field(default_factory=dict)
    latest: str = "v1164"
    broken: tuple = ()  # lookups that fail with a server error: "release", "tag", "latest"


class PublishReleaseTests(unittest.TestCase):
    def publish(self, ref, world=None):
        world = world or GitHub()
        root = Path(tempfile.mkdtemp())
        self.addCleanup(lambda: subprocess.run(["rm", "-rf", str(root)], check=False))
        (root / "bin").mkdir()
        gh = root / "bin" / "gh"
        gh.write_text(FAKE_GH.replace("PYTHON", sys.executable))
        gh.chmod(0o755)
        (root / "state.json").write_text(json.dumps({"releases": world.releases, "tags": world.tags, "latest": world.latest,
                                                         "broken": list(world.broken)}))
        (root / "release").mkdir()
        (root / "release" / "asset.tar.gz").write_text("x")
        env = dict(os.environ, PATH=f"{root / 'bin'}:{os.environ['PATH']}", GH_STATE=str(root / "state.json"),
                   GH_CALLS=str(root / "calls"), GITHUB_REPOSITORY="pairtask/pairtask", GITHUB_REF=ref,
                   GITHUB_REF_NAME=ref.rsplit("/", 1)[1], GITHUB_SHA=SHA, GITHUB_RUN_NUMBER=str(world.run))
        done = subprocess.run(["bash", str(SCRIPT)], cwd=root, env=env, capture_output=True, text=True)
        calls = [json.loads(line) for line in (root / "calls").read_text().splitlines()] if (root / "calls").exists() else []
        return done, calls

    def writes(self, calls):
        return [c for c in calls if c[:2] in (["release", "create"], ["release", "edit"], ["release", "delete"])]

    def test_main_publishes_vN_as_latest(self):
        done, calls = self.publish("refs/heads/main")
        self.assertEqual(done.returncode, 0, done.stderr)
        self.assertIn(["release", "edit", "v1165", "--repo", "pairtask/pairtask", "--draft=false", "--latest"], calls)

    def test_a_published_release_is_never_replaced(self):
        done, calls = self.publish("refs/heads/main", GitHub(releases={"v1165": {"draft": False}}))
        self.assertNotEqual(done.returncode, 0)
        self.assertEqual(self.writes(calls), [])

    def test_a_draft_is_redone_and_its_tag_kept(self):
        done, calls = self.publish("refs/heads/main", GitHub(releases={"v1165": {"draft": True}}))
        self.assertEqual(done.returncode, 0, done.stderr)
        deletes = [c for c in calls if c[:2] == ["release", "delete"]]
        self.assertEqual(len(deletes), 1)
        self.assertNotIn("--cleanup-tag", deletes[0])

    def test_a_hotfix_of_an_older_line_does_not_become_latest(self):
        done, calls = self.publish("refs/tags/v1160.1", GitHub(tags={"v1160.1": SHA}, latest="v1165"))
        self.assertEqual(done.returncode, 0, done.stderr)
        self.assertIn(["release", "edit", "v1160.1", "--repo", "pairtask/pairtask", "--draft=false", "--latest=false"], calls)

    def test_a_main_run_finishing_out_of_order_does_not_become_latest(self):
        done, calls = self.publish("refs/heads/main", GitHub(run=1164, latest="v1165"))
        self.assertEqual(done.returncode, 0, done.stderr)
        self.assertIn("--latest=false", calls[-1])

    def test_a_new_tag_follows_a_legacy_latest(self):
        done, calls = self.publish("refs/heads/main", GitHub(latest="workspace-v0.0.0-build.1160.1"))
        self.assertEqual(done.returncode, 0, done.stderr)
        self.assertEqual(calls[-1][-1], "--latest")

    def test_a_bare_vN_tag_push_is_refused(self):
        done, calls = self.publish("refs/tags/v1160")
        self.assertNotEqual(done.returncode, 0)
        self.assertEqual(self.writes(calls), [])

    def test_a_tag_on_another_commit_is_refused(self):
        done, calls = self.publish("refs/tags/v1165.1", GitHub(tags={"v1165.1": "b" * 40}))
        self.assertNotEqual(done.returncode, 0)
        self.assertEqual(self.writes(calls), [])

    def test_a_tag_mismatch_is_found_before_a_draft_is_touched(self):
        world = GitHub(releases={"v1160.1": {"draft": True}}, tags={"v1160.1": "b" * 40})
        done, calls = self.publish("refs/tags/v1160.1", world)
        self.assertNotEqual(done.returncode, 0)
        self.assertEqual(self.writes(calls), [])

    def test_a_failing_lookup_stops_before_any_write(self):
        for broken in ("release", "tag", "latest"):
            done, calls = self.publish("refs/tags/v1160.1", GitHub(tags={"v1160.1": SHA}, latest="v1165", broken=(broken,)))
            self.assertNotEqual(done.returncode, 0, broken)
            self.assertEqual(self.writes(calls), [], broken)

    def test_no_latest_yet_makes_the_first_release_latest(self):
        done, calls = self.publish("refs/heads/main", GitHub(latest=""))
        self.assertEqual(done.returncode, 0, done.stderr)
        self.assertEqual(calls[-1][-1], "--latest")

    def test_a_legacy_tag_that_cant_be_ordered_never_becomes_latest(self):
        done, calls = self.publish("refs/tags/workspace-v1.2.3", GitHub(latest="v1165"))
        self.assertEqual(done.returncode, 0, done.stderr)
        self.assertEqual(calls[-1][-1], "--latest=false")


if __name__ == "__main__":
    unittest.main()
