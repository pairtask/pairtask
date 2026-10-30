"""Reject false positives in the deployed agent-routing proof."""

from contextlib import redirect_stdout
import io
import json
from pathlib import Path
import sys
from types import SimpleNamespace
import unittest
from unittest.mock import patch

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import pairing_agent_checks as checks


class ExchangeFixture:
    def __init__(self, bad=None, delay=0):
        self.now = 0
        self.bad = bad
        self.delay = delay
        self.args = None
        self.sent = []
        self.agent = SimpleNamespace(await_frame=self.await_frame, acknowledge=lambda _: None, send=self.sent.append)
        self.secured = SimpleNamespace(start=self.start, message=self.message)
        self.sides = ((None, "host-key"), (None, "secured-key"))

    def start(self, *args):
        self.args = args

    def await_frame(self, wanted, deadline):
        frame = {"type": "msg", "origin": "remote", "from": checks.SECURED_AGENT,
                 "message_id": "remote:incoming", "payload": {"text": "hello first-fixed"},
                 "remote": {"contact": "secured-key", "sender": checks.SECURED_AGENT}}
        if wanted(frame):
            return frame
        for sent in self.sent:
            ack = {"type": "ack", "message_id": sent["message_id"], "stage": "accepted"}
            if wanted(ack):
                return ack
        raise AssertionError("proof requested an unexpected frame")

    def message(self):
        self.now += self.delay
        frame = {"type": "msg", "origin": "remote", "from": checks.AGENT,
                 "in_reply_to": self.args[4], "payload": {"text": "hi answer-fixed"},
                 "remote": {"contact": "host-key", "sender": checks.AGENT}}
        if self.bad == "origin":
            frame["origin"] = "operator"
        if self.bad == "contact":
            frame["remote"]["contact"] = "different-workspace"
        if self.bad == "sender":
            frame["remote"]["sender"] = "other-agent"
        if self.bad == "text":
            frame["payload"]["text"] = "unrelated reply"
        return frame

    def run(self):
        with patch.object(checks.time, "monotonic", lambda: self.now), \
             patch.object(checks.uuid, "uuid4", return_value=SimpleNamespace(hex="fixed")), \
             redirect_stdout(io.StringIO()):
            checks.secured_first(self.sides, self.agent, self.secured)


def run_secured_operator_script(late=False):
    """Execute the shipped script against a broker-shaped socket."""
    now = [0]
    answer = {"type": "msg", "origin": "remote", "from": "operator",
              "message_id": "remote:operator-answer", "payload": {"text": "operator answer"},
              "remote": {"contact": "host-key", "sender": ""}}
    frames = [{"type": "welcome"},
              {"type": "ack", "message_id": "first", "stage": "accepted"}, answer]

    def readline():
        if not frames:
            return ""
        frame = frames.pop(0)
        if late and frame is answer:
            now[0] = 100
        return json.dumps(frame) + "\n"

    sock = SimpleNamespace(connect=lambda _: None, sendall=lambda _: None,
                           settimeout=lambda _: None, makefile=lambda *a, **kw: SimpleNamespace(readline=readline))
    module = SimpleNamespace(AF_UNIX=1, socket=lambda _: sock)
    argv = ["proof.py", "first", checks.SECURED_AGENT, "host-key", "operator",
            "first", "hello", "-", "operator answer", "40"]
    out = io.StringIO()
    with patch.dict(sys.modules, {"socket": module}), patch.object(sys, "argv", argv), \
         patch.object(checks.time, "monotonic", lambda: now[0]), redirect_stdout(out):
        exec(compile(checks.SECURED_AGENT_SCRIPT, "secured-agent-proof", "exec"), {})
    return json.loads(out.getvalue())


class AgentProofAcceptance(unittest.TestCase):
    def test_secured_script_accepts_operator_answer_without_parent(self):
        result = run_secured_operator_script()
        self.assertEqual(result["got"]["from"], "operator")
        self.assertNotIn("in_reply_to", result["got"])

    def test_secured_script_rejects_frame_read_after_deadline(self):
        with self.assertRaises(SystemExit):
            run_secured_operator_script(late=True)

    def test_valid_exchange(self):
        ExchangeFixture().run()

    def test_remote_reply_must_preserve_provenance_and_exact_text(self):
        for field in ("origin", "contact", "sender", "text"):
            with self.subTest(field=field), self.assertRaises(ValueError):
                ExchangeFixture(bad=field).run()

    def test_slow_secured_result_cannot_pass(self):
        with self.assertRaises(ValueError):
            ExchangeFixture(delay=checks.INSTANT * 4).run()


if __name__ == "__main__":
    unittest.main()
