"""Admission and rollout must share the update transaction's stable lock."""

from contextlib import ExitStack
from pathlib import Path
import runpy
import sys
from types import SimpleNamespace
import unittest
from unittest.mock import patch

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import sandbox_tools_update as update
import sandbox_rollout as rollout
import test_tools_update_host_acceptance as host_fixtures
import test_sandbox_rollout as rollout_fixtures

SCRIPTS = Path(update.__file__).parent


class UpdateAdmissionAcceptance(unittest.TestCase):
    def setUp(self):
        self.rig = host_fixtures.HostAcceptance()
        self.rig.setUp()
        self.addCleanup(self.rig.doCleanups)
        self.directory = self.rig.journal.path.parent
        self.main = runpy.run_path(str(SCRIPTS / "sandbox-session.py"))["main"]

    def session(self, args):
        with patch.dict(self.main.__globals__, {
            "existing_deployment": lambda *_a, **_k: self.directory,
            "run_session": lambda *_a, **_k: self.fail("a session was admitted"),
        }):
            self.assertEqual(self.main(args), 75)

    def test_shell_refuses_held_update_lock_before_first_journal_write(self):
        with self.rig.journal:
            self.session(["shell"])

    def test_shell_refuses_malformed_journal_instead_of_treating_it_as_absent(self):
        self.rig.journal.path.write_text("{broken")
        self.session(["shell"])
        self.assertEqual(self.rig.journal.path.read_text(), "{broken")

    def test_secured_session_cannot_bypass_committing_update(self):
        self.rig.stop_at("flipping")
        self.session(["secured", "--project", "proof"])

    def test_session_lock_covers_spawn_but_not_the_running_child(self):
        def wait():
            with update.journal_in(self.directory):
                return 23
        def spawn(_argv):
            with self.assertRaises(update.UpdateRefused):
                with update.journal_in(self.directory):
                    pass
            return SimpleNamespace(wait=wait)
        with patch.dict(self.main.__globals__, {
            "existing_deployment": lambda *_a, **_k: self.directory,
            "subprocess": SimpleNamespace(Popen=spawn),
        }):
            self.assertEqual(self.main(["shell"]), 23)

    def test_start_refuses_update_before_listing_or_starting_containers(self):
        main = runpy.run_path(str(SCRIPTS / "pairtask.py"))["main"]
        with self.rig.journal, patch.dict(main.__globals__, {
            "existing_deployment": lambda *_a, **_k: self.directory,
            "refuse_root": lambda *_: False,
            "execute": lambda *_: self.fail("start reached container execution"),
        }):
            self.assertEqual(main(["start", "--remote", "local"]), 75)

    def test_ssh_setup_refuses_update_before_key_or_container_changes(self):
        main = runpy.run_path(str(SCRIPTS / "sandbox-ssh.py"))["main"]
        with self.rig.journal, patch.object(sys, "argv", ["sandbox-ssh.py"]), patch.dict(main.__globals__, {
            "existing_deployment": lambda *_a, **_k: self.directory,
            "setup": lambda *_: self.fail("SSH setup reached key installation"),
        }):
            with self.assertRaises(update.UpdateRefused):
                main()


class UpdateRolloutAcceptance(unittest.TestCase):
    def setUp(self):
        self.fixture = rollout_fixtures.RolloutTests()
        self.fixture.setUp()
        self.addCleanup(self.fixture.doCleanups)

    def run_rollout(self, stop):
        fixture = self.fixture
        with ExitStack() as stack:
            for name, replacement in {
                "download_release": lambda *_: fixture.image,
                "stop_managed": stop,
                "run_locked_operation": lambda *_: None,
                "install_tools": lambda *_: None,
                "configure_ssh": lambda *_: None,
            }.items():
                stack.enter_context(patch.object(rollout, name, side_effect=replacement))
            return rollout.rollout(fixture.args, fixture.root, fixture.server)

    def test_rollout_keeps_update_excluded_through_container_replacement(self):
        def at_stop(*_):
            with self.assertRaises(update.UpdateRefused):
                with update.journal_in(self.fixture.root):
                    pass
        self.run_rollout(at_stop)

    def test_unfinished_update_refuses_rollout_without_replacing_or_superseding(self):
        rig = host_fixtures.HostAcceptance()
        rig.setUp()
        self.addCleanup(rig.doCleanups)
        rig.stop_at("flipping")
        journal = update.journal_in(self.fixture.root)
        original = rig.journal.path.read_bytes()
        journal.path.write_bytes(original)
        with self.assertRaises(update.UpdateRefused):
            self.run_rollout(lambda *_: self.fail("rollout stopped containers with an unfinished update"))
        self.assertEqual(journal.path.read_bytes(), original)


if __name__ == "__main__":
    unittest.main()
