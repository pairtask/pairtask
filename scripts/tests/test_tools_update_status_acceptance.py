"""Retained generations are visible without following artifact links or changing state."""

import contextlib
import io
import os
import runpy
from types import SimpleNamespace
from unittest import mock
from pathlib import Path
import sys
import unittest

ROOT = Path(__file__).resolve().parents[2]
sys.path[:0] = [str(ROOT / "scripts"), str(ROOT / "infra/image")]
import test_tool_update_acceptance as fixtures


class RetainedStatusAcceptance(unittest.TestCase):
    def setUp(self):
        self.fixture = fixtures.UpdateAcceptance()
        self.fixture.setUp()
        self.addCleanup(self.fixture.doCleanups)
        self.store = self.fixture.store

    def retained(self):
        data = self.store.status()
        self.assertIn("retained", data, "status must expose the cost of keeping generations")
        return data["retained"]

    def test_current_and_previous_generations_are_counted_without_mutation(self):
        self.assertEqual(self.retained()["generations"], 1)
        self.fixture.commit()
        self.fixture.commit("next", "3.0.0")
        before = fixtures.tree_contents(self.store.root)
        state = self.store.state_path.read_bytes()
        info = self.retained()
        self.assertEqual(info["generations"], 3)
        self.assertGreater(info["stored_bytes"], 0)
        self.assertEqual(fixtures.tree_contents(self.store.root), before)
        self.assertEqual(self.store.state_path.read_bytes(), state)

    def test_shared_files_count_once_and_external_links_are_not_followed(self):
        before = self.retained()["stored_bytes"]
        payload = self.store.root / "accounting-fixture"
        payload.write_bytes(b"x" * 129)
        os.link(payload, self.store.root / "accounting-hardlink")
        outside = self.store.root.parent / "outside-accounting"
        outside.mkdir()
        (outside / "large").write_bytes(b"z" * 65536)
        (self.store.root / "external-link").symlink_to(outside, target_is_directory=True)
        self.assertEqual(self.retained()["stored_bytes"], before + 129)
        # A generation alias is not another retained generation.
        (self.store.generations / "alias").symlink_to(self.store.generations / self.fixture.old, target_is_directory=True)
        self.assertEqual(self.retained()["generations"], 1)

    def test_staging_reports_retained_candidate_without_moving_current(self):
        candidate = self.fixture.staged()
        self.assertEqual(self.retained()["generations"], 2)
        self.assertEqual(self.store.current_generation(), self.fixture.old)
        self.assertTrue((self.store.generations / candidate).is_dir())
        self.fixture.updates.abort("update-a")
        self.assertEqual(self.retained()["generations"], 1)


class UpdateStatusDiscoveryAcceptance(unittest.TestCase):
    def test_status_does_not_create_a_missing_deployment(self):
        module = runpy.run_path(str(ROOT / "scripts/sandbox-tools-update.py"), run_name="status_acceptance")
        scope = module["main"].__globals__
        create = mock.Mock(side_effect=AssertionError("read-only status created a deployment"))
        existing = mock.Mock(return_value=None)
        work = SimpleNamespace(status=lambda: {"journal": None})
        replacements = {"deployment_directory": create, "existing_deployment": existing,
                        "transaction": lambda *_: work, "refuse_root": lambda *_: False,
                        "default_remote": lambda: "local"}
        with mock.patch.dict(scope, replacements), contextlib.redirect_stdout(io.StringIO()):
            self.assertEqual(module["main"](["--status", "--remote", "local"]), 0)
        create.assert_not_called()
        existing.assert_called_once()


if __name__ == "__main__":
    unittest.main()
