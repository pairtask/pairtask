"""Auto-recoverable and terminal records must be validated before use."""

import unittest

import test_tools_update_host_acceptance as fixtures

host = fixtures.host


class JournalRecoveryAcceptance(unittest.TestCase):
    def rig(self):
        rig = fixtures.HostAcceptance()
        rig.setUp()
        self.addCleanup(rig.doCleanups)
        return rig

    def test_failed_release_requires_its_durable_target_and_candidate(self):
        for field in ("target", "candidate"):
            with self.subTest(field=field):
                rig = self.rig()
                record = rig.update(fixtures.Interrupt(failure=("secured", "release"))).run({})
                self.assertEqual(record["failure"], "release")
                destination = record["containers"]["secured"] if field == "candidate" else record
                destination[field] = None
                rig.journal.write(record)
                original = rig.journal.path.read_bytes()
                rig.events.clear()
                with self.assertRaises(host.JournalError):
                    rig.update().recover()
                self.assertEqual(rig.events, [])
                self.assertEqual(rig.journal.path.read_bytes(), original)

    def test_partial_rollback_map_refuses_before_opening_a_new_transaction(self):
        rig = self.rig()
        record = rig.update().run({})
        # An aborted attempt preserves the last completed rollback pair.
        record.update(phase="aborted", outcome=None, rollback={"workspace": rig.old["workspace"]})
        rig.journal.write(record)
        original = rig.journal.path.read_bytes()
        rig.events.clear()
        with self.assertRaises(host.JournalError):
            rig.update().rollback()
        self.assertEqual(rig.events, [])
        self.assertEqual(rig.journal.path.read_bytes(), original)


if __name__ == "__main__":
    unittest.main()
