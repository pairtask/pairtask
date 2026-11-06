"""Setup's scoped Claude tool permissions; runtime policy remains separate."""

import importlib.util
import json
from pathlib import Path
import re
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[2]
SETTINGS = ROOT / "infra/image/claude-settings.json"
TOOLS = {
    "send", "acknowledge", "receive", "wait", "wait_reply", "listen", "listener_status",
    "status", "read_status", "claim", "finish_claim", "release_claim", "read_claims",
    "join_room", "leave_room", "list_rooms", "workspaces",
}


class ClaudePermissionsAcceptance(unittest.TestCase):
    def test_only_the_exact_registered_collaboration_tools_are_allowed(self):
        self.assertTrue(SETTINGS.is_file(), "setup does not ship the scoped Claude settings")
        settings = json.loads(SETTINGS.read_text())
        self.assertEqual(set(settings), {"permissions"})
        self.assertEqual(set(settings["permissions"]), {"allow"})
        allowed = settings["permissions"]["allow"]
        self.assertCountEqual(allowed, ["mcp__pairtask__" + name for name in TOOLS])
        source = "\n".join(p.read_text() for p in (ROOT / "internal/bridge").glob("*.go")
                           if not p.name.endswith("_test.go"))
        registered = set(re.findall(r'Name:\s*"([^"]+)"', source))
        self.assertTrue(TOOLS <= registered, f"unregistered allowlist tools: {TOOLS - registered}")
        servers = json.loads((ROOT / "infra/image/claude-mcp.json").read_text())["mcpServers"]
        self.assertIn("pairtask", servers, "permission names must match the deployed server")

    def test_installer_stages_the_settings_at_the_launchers_path(self):
        installer = (ROOT / "infra/image/install.sh").read_text().replace("\\\n", " ")
        self.assertRegex(installer, r"(?:cp|install)\b[^\n]*infra/image/claude-settings\.json"
                         r"[^\n]*/etc/pairtask/(?:claude-settings\.json)?")

    def test_tool_activation_changes_servers_without_mutating_existing_settings(self):
        spec = importlib.util.spec_from_file_location("permissions_tool_store", ROOT / "infra/image/tool_store.py")
        store = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(store)
        with tempfile.TemporaryDirectory() as directory:
            share, etc = Path(directory) / "share", Path(directory) / "etc"
            share.mkdir()
            (etc / "pairtask").mkdir(parents=True)
            settings = etc / "pairtask/claude-settings.json"
            original = b'{"permissions":{"allow":["mcp__pairtask__send"],"deny":["Bash(*)"]}}\n'
            settings.write_bytes(original)
            (share / "mcp-servers.json").write_text(json.dumps({"codescene": {"command": "fixture-codescene"}}))
            (share / "claude-mcp.base.json").write_text(json.dumps({"mcpServers": {"pairtask": {"command": "fixture-pairtask"}}}))
            for enabled in (True, False):
                with self.subTest(optional_server_enabled=enabled):
                    store.write_configuration({"cs-mcp": enabled}, share=share, etc=etc)
                    self.assertEqual(settings.read_bytes(), original)
                    servers = json.loads((etc / "pairtask/claude-mcp.json").read_text())["mcpServers"]
                    self.assertEqual(set(servers), {"pairtask", "codescene"} if enabled else {"pairtask"})


if __name__ == "__main__":
    unittest.main()
