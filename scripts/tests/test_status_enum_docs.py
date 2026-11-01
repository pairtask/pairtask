"""Keep MCP status enum tables aligned with the broker's allowed values."""

import json
from pathlib import Path
import re
import unittest


ROOT = Path(__file__).resolve().parents[2]
PROTOCOL = ROOT / "internal/protocol/agent_status.go"
GUIDE = ROOT / "docs/mcp.md"


def protocol_values(name):
    source = PROTOCOL.read_text(encoding="utf-8")
    match = re.search(r"\b" + re.escape(name) + r"\s*=\s*\[\]string\{([^}]+)\}", source)
    if match is None:
        raise AssertionError("missing protocol enum " + name)
    return [json.loads(value) for value in re.findall(r'"(?:[^"\\]|\\.)*"', match.group(1))]


class StatusEnumDocsTest(unittest.TestCase):
    def table_values(self, table):
        page = GUIDE.read_text(encoding="utf-8")
        start = "<!-- status-" + table + ":start -->"
        end = "<!-- status-" + table + ":end -->"
        self.assertEqual(page.count(start), 1, "missing or duplicate " + start)
        self.assertEqual(page.count(end), 1, "missing or duplicate " + end)
        body = page.split(start, 1)[1].split(end, 1)[0]
        rows = re.findall(r"^\|\s*`([^`]+)`\s*\|\s*([^|\n]+)\|\s*$", body, re.MULTILINE)
        self.assertTrue(rows, "status enum table has no documented values")
        values = []
        for value, meaning in rows:
            self.assertTrue(meaning.strip(), "each enum needs a meaning")
            values.append("" if value == '""' else value)
        return values

    def test_status_states_match_protocol(self):
        self.assertEqual(self.table_values("states"), protocol_values("StatusStates"))

    def test_waiting_on_matches_protocol_including_empty(self):
        self.assertEqual(self.table_values("waiting"), protocol_values("StatusWaiting"))


if __name__ == "__main__":
    unittest.main()
