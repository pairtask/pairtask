"""Browser-style release tags (vN, vN.P) are read next to the legacy workspace-v0.0.0-build.N.A."""

from pathlib import Path
import sys
import unittest

SCRIPTS = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(SCRIPTS))
import sandbox_releases  # noqa: E402
import sandbox_version_lookup as lookup  # noqa: E402


class NumberedReleaseTagTests(unittest.TestCase):
    def test_both_forms_parse_and_order_by_build(self):
        legacy = lookup.parse_build("workspace-v0.0.0-build.1160.1")
        retried = lookup.parse_build("workspace-v0.0.0-build.1141.2")
        new = lookup.parse_build("v1161")
        hotfix = lookup.parse_build("v1161.1")
        self.assertEqual((legacy, retried, new, hotfix), ((1160, 0), (1141, 1), (1161, 0), (1161, 1)))
        self.assertLess(legacy, new)
        self.assertLess(new, hotfix)

    def test_other_tags_are_not_releases(self):
        for tag in ("v0", "v1161.0", "1161", "v1161-beta", "workspace-v1.0.0", "", None):
            self.assertIsNone(lookup.parse_build(tag), tag)
            self.assertFalse(lookup.is_tag(tag), tag)

    def test_names(self):
        self.assertEqual(lookup.release_name("workspace-v0.0.0-build.1160.1"), "build 1160.1")
        self.assertEqual(lookup.release_name("v1161"), "Pairtask 1161")
        self.assertEqual(lookup.release_name("v1161.2"), "Pairtask 1161.2")

    def test_a_release_can_be_selected_by_either_form(self):
        for tag in ("latest", "v1161", "v1161.1", "workspace-v0.0.0-build.1160.1"):
            self.assertEqual(sandbox_releases.release_tag(tag), tag)
        for tag in ("v0", "1161", "v1161-rc"):
            with self.assertRaises(ValueError):
                sandbox_releases.release_tag(tag)


if __name__ == "__main__":
    unittest.main()
