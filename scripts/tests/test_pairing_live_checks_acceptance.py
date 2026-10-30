"""The deployed proof must observe reconnects and reject duplicate delivery."""

from contextlib import redirect_stdout
import io
import json
from pathlib import Path
import sys
from types import SimpleNamespace
import unittest
from unittest.mock import patch

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import pairing_live_checks as live


class LinkProofFixture:
    def __init__(self, duplicate=False):
        self.inboxes = {"secured": [], "host": []}
        self.calls = []
        self.after_restart_needs_observation = False
        self.duplicate = duplicate
        self.generation = 0

    def side(self, name):
        def run(*args):
            self.calls.append((name, args))
            assert args[0] == "contact"
            operation = args[1]
            if operation == "list":
                self.after_restart_needs_observation = False
                body = {"contact_page": {"sync": {"live": True, "dials": name == "host", "last_attempt": str(self.generation)}, "messages": self.inboxes[name]}}
            elif operation == "sync":
                body = {"contact_sync": {"live": True}}
            elif operation == "send":
                assert not self.after_restart_needs_observation, "send triggered reconnect before passive observation"
                recipient = "host" if name == "secured" else "secured"
                message_id = args[args.index("--message-id") + 1] if "--message-id" in args else str(len(self.calls))
                row = {"id": message_id, "text": args[3], "direction": "in", "state": "received"}
                self.inboxes[recipient].extend([row] * (2 if self.duplicate else 1))
                body = {"contact_message": {"id": message_id, "state": "queued"}}
            else:
                raise AssertionError(operation)
            return SimpleNamespace(returncode=0, stdout=json.dumps(body), stderr="")
        return run

    def restart(self):
        self.generation += 1
        self.after_restart_needs_observation = True


class LiveProofAcceptance(unittest.TestCase):
    def test_reconnect_is_observed_before_send_without_sync_wakes(self):
        fixture = LinkProofFixture()
        with redirect_stdout(io.StringIO()):
            live.prove_live(((fixture.side("secured"), "host-key"), (fixture.side("host"), "secured-key")), fixture.restart)
        self.assertFalse(any(args[1] == "sync" for _, args in fixture.calls), "proof woke the link it claimed reconnected itself")
        self.assertEqual(sum(args[1] == "send" for _, args in fixture.calls), 4)

    def test_stale_live_flag_does_not_prove_reconnection(self):
        fixture = LinkProofFixture()
        def restart_without_redial():
            fixture.after_restart_needs_observation = True
        with patch.object(live.time, "sleep"), patch.object(live.time, "monotonic", side_effect=range(200)), redirect_stdout(io.StringIO()):
            with self.assertRaises(ValueError):
                live.prove_live(((fixture.side("secured"), "host-key"), (fixture.side("host"), "secured-key")), restart_without_redial)

    def test_duplicate_rows_cannot_count_as_successful_delivery(self):
        fixture = LinkProofFixture(duplicate=True)
        sides = ((fixture.side("secured"), "host-key"), (fixture.side("host"), "secured-key"))
        with patch.object(live.time, "sleep"), patch.object(live.time, "monotonic", side_effect=range(100)), redirect_stdout(io.StringIO()):
            with self.assertRaises(ValueError):
                live.exchange(sides, "duplicate must fail")


if __name__ == "__main__":
    unittest.main()
