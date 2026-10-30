"""The deployed routing proof correlates receipts and enforces real deadlines."""

from collections import deque
from contextlib import redirect_stdout
import io
import json
from pathlib import Path
import sys
from types import SimpleNamespace
import unittest
from unittest.mock import patch

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import pairing_routing_checks as routing


class RoutingProofFixture:
    def __init__(self, confirmation=True, duplicate=False, delay=0):
        self.now = 0
        self.confirmation = confirmation
        self.duplicate = duplicate
        self.delay = delay
        self.frames = deque()
        self.rows = [{"id": "ping", "direction": "out", "agent_acks": [routing.AGENT]}]
        self.agent = routing.Agent.__new__(routing.Agent)
        self.agent.sock = SimpleNamespace(sendall=self.send, settimeout=lambda timeout: None)
        self.agent.reader = SimpleNamespace(readline=self.readline)
        self.sides = ((self.run, "host-key"), (self.run, "secured-key"))

    def clock(self):
        return self.now

    def sleep(self, seconds):
        self.now += seconds

    def readline(self):
        return json.dumps(self.frames.popleft()) + "\n" if self.frames else ""

    def send(self, data):
        msg = json.loads(data)
        if msg["type"] == "ack":
            self.confirm_ack(msg)
        if msg["type"] == "msg":
            self.frames.append({"type": "ack", "message_id": msg["message_id"], "stage": "accepted"})
            row = {"id": msg["message_id"], "direction": "in", "text": msg["payload"]["text"],
                   "from": routing.AGENT, "reply_id": "ping", "reply_side": "by_receiver"}
            self.rows.extend([row] * (2 if self.duplicate else 1))

    def confirm_ack(self, msg):
        if not self.confirmation:
            return
        if msg["stage"] == "agent_acknowledged":
            self.frames.append({"type": "ack", "message_id": msg["message_id"], "stage": "agent_acknowledged"})

    def run(self, *args):
        assert args[:2] == ("contact", "list"), args
        self.now += self.delay
        return SimpleNamespace(returncode=0, stdout=json.dumps({"contact_page": {"messages": self.rows}}), stderr="")

    def reply(self):
        with patch.object(routing.time, "monotonic", self.clock), patch.object(routing.time, "sleep", self.sleep), redirect_stdout(io.StringIO()):
            routing.reply_back(self.sides, self.agent, "ping", {"message_id": "remote:imported"})


class RoutingProofAcceptance(unittest.TestCase):
    def test_prior_agent_ack_confirmation_is_not_reply_acceptance(self):
        RoutingProofFixture().reply()

    def test_duplicate_reply_rows_cannot_prove_delivery(self):
        with self.assertRaises(ValueError):
            RoutingProofFixture(confirmation=False, duplicate=True).reply()

    def test_slow_reply_cannot_pass_with_a_fresh_receive_budget(self):
        with self.assertRaises(ValueError):
            RoutingProofFixture(confirmation=False, delay=routing.INSTANT + 1).reply()

    def test_matching_frame_after_requested_deadline_is_rejected(self):
        fixture = RoutingProofFixture()

        def late_frame():
            fixture.now += 2
            return json.dumps({"type": "msg", "message_id": "late"}) + "\n"

        fixture.agent.reader.readline = late_frame
        with patch.object(routing.time, "monotonic", fixture.clock):
            with self.assertRaises((ValueError, TimeoutError)):
                fixture.agent.next_of("msg", 1)

    def test_import_cannot_claim_local_operator_or_another_workspace(self):
        base = {"origin": "remote", "from": "operator", "remote": {"contact": "secured-key", "sender": ""}, "payload": {"text": "ping ping"}}
        for change in ({"origin": "operator"}, {"remote": {"contact": "other-key"}}, {"from": "someone"}, {"payload": {"text": "wrong"}}):
            with self.subTest(change=change), self.assertRaises(ValueError):
                routing.check_import(dict(base, **change), "secured-key", ("ping", "ping ping"))


if __name__ == "__main__":
    unittest.main()
