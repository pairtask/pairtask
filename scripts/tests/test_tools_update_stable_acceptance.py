"""Operator-selected Claude stable default; explicit versions still win."""
from pathlib import Path
import sys
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[2]
sys.path[:0] = [str(ROOT / "scripts"), str(ROOT / "infra/image")]
import sandbox_tools_plan as plan
import sandbox_versions as versions


class StableUpdateAcceptance(unittest.TestCase):
    def test_omitted_claude_uses_stable_channel(self):
        with patch.object(versions, "publisher_text", return_value="2.1.311") as fetch:
            self.assertEqual(plan.wanted_versions({}, ["claude"]), {"claude": "2.1.311"})
        self.assertEqual(fetch.call_count, 1)
        self.assertTrue(fetch.call_args.args[0].endswith("/stable"), fetch.call_args)

    def test_exact_claude_does_not_consult_any_channel(self):
        with patch.object(versions, "publisher_text", side_effect=AssertionError("channel consulted")):
            self.assertEqual(plan.wanted_versions({"claude": "2.1.328"}, ["claude"]), {"claude": "2.1.328"})

    def test_invalid_stable_answer_does_not_fall_back_to_latest(self):
        with patch.object(versions, "publisher_text", return_value="moving-target") as fetch:
            with self.assertRaises(ValueError):
                plan.wanted_versions({}, ["claude"])
        self.assertEqual(fetch.call_count, 1)
        self.assertTrue(fetch.call_args.args[0].endswith("/stable"), fetch.call_args)

    def test_other_clients_keep_release_defaults(self):
        def release(url):
            return {"tag_name": "rust-v0.170.0" if "openai/codex" in url else "MCP-1.5.22"}
        with patch.object(versions, "publisher_json", side_effect=release) as fetch:
            self.assertEqual(plan.wanted_versions({}, ["codex", "cs-mcp"]), {"codex": "0.170.0", "cs-mcp": "1.5.22"})
        self.assertEqual(fetch.call_count, 2)
        self.assertTrue(all(call.args[0].endswith("/releases/latest") for call in fetch.call_args_list))


if __name__ == "__main__":
    unittest.main()
