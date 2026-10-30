"""Reject mismatched results in the deployed review proof."""

import copy
from pathlib import Path
import sys
import unittest

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import pairing_review_checks as checks


def result():
    return {
        "type": "msg", "origin": "remote", "from": "reviewer",
        "in_reply_to": "ask", "remote": {"contact": "workspace", "sender": "reviewer"},
        "review_outcome": {
            "ask_id": "ask", "reviewer": "reviewer", "state": "completed",
            "repository": checks.REPOSITORY, "change": checks.CHANGE,
            "verdict": "comment", "summary": checks.VERDICT["summary"],
            "attempt": 1, "intent": 0,
        },
    }


class ReviewProofAcceptance(unittest.TestCase):
    def test_valid_result(self):
        checks.check_outcome(result(), ("reviewer", "workspace"), "ask", "completed")

    def test_wrong_parent_or_remote_sender_rejected(self):
        for field in ("parent", "sender", "repository", "summary", "intent"):
            with self.subTest(field=field):
                msg = copy.deepcopy(result())
                if field == "parent":
                    msg["in_reply_to"] = "another-ask"
                elif field == "sender":
                    msg["remote"]["sender"] = "another-reviewer"
                elif field == "repository":
                    msg["review_outcome"]["repository"] = "another/repository"
                elif field == "summary":
                    msg["review_outcome"]["summary"] = "unrelated verdict"
                else:
                    msg["review_outcome"]["intent"] = 1
                with self.assertRaises((SystemExit, AssertionError, RuntimeError, ValueError)):
                    checks.check_outcome(msg, ("reviewer", "workspace"), "ask", "completed")


if __name__ == "__main__":
    unittest.main()
