"""Keep the operator chat guide's pasteable example tied to its tested fixture."""

from pathlib import Path
import unittest


ROOT = Path(__file__).resolve().parents[2]
GUIDE = ROOT / "docs/chat-formatting.md"
EXAMPLE = ROOT / "internal/chat/testdata/formatting-guide-message.txt"
START = "<!-- chat-formatting-example:start -->\n````text\n"
END = "\n````\n<!-- chat-formatting-example:end -->"


class ChatFormattingGuideTest(unittest.TestCase):
    def test_pasteable_example_matches_renderer_fixture(self):
        page = GUIDE.read_text()
        self.assertEqual(page.count(START), 1)
        self.assertEqual(page.count(END), 1)
        actual = page.split(START, 1)[1].split(END, 1)[0]
        self.assertEqual(actual + "\n", EXAMPLE.read_text())


if __name__ == "__main__":
    unittest.main()
