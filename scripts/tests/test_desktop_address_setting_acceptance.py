"""The Desktop extension exposes a human-editable address setting."""

import json
from pathlib import Path
import unittest


ROOT = Path(__file__).resolve().parents[2]


class DesktopAddressSettingAcceptance(unittest.TestCase):
    def test_extension_passes_workspace_address_setting_to_its_node(self):
        manifest = json.loads((ROOT / "desktop" / "extension" / "manifest.json").read_text())
        setting = manifest["user_config"].get("address")
        self.assertIsNotNone(setting, "Desktop has no Workspace address setting")
        self.assertEqual(setting["type"], "string")
        self.assertFalse(setting.get("required", False))
        self.assertIn("address", setting["title"].lower())
        self.assertIn("HOST:PORT", setting["description"])
        self.assertIn("--address=${user_config.address}", manifest["server"]["mcp_config"]["args"])


if __name__ == "__main__":
    unittest.main()
