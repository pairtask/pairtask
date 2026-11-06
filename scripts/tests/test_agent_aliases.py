from pathlib import Path
import shlex
import subprocess
import unittest


ALIASES = Path(__file__).resolve().parents[2] / "infra/image/pairtask-agent-aliases.sh"


class AgentAliasTests(unittest.TestCase):
    def shell(self, command, uid=1001, interactive=True):
        setup = f'id() {{ printf "%s\\n" {uid}; }}\n. {shlex.quote(str(ALIASES))}\n'
        return subprocess.run(["bash", "--noprofile", "--norc", "-ic" if interactive else "-c",
                               setup + command], check=True, capture_output=True, text=True).stdout.splitlines()

    def test_codex_forwards_resume_arguments_and_pins_native_binary(self):
        output = self.shell('function /usr/local/lib/pairtask/pairtask-codex() { printf "%s\\n" "$@"; }\n'
                            'codex resume "session with spaces" -C "/workspace/my repo"')
        self.assertEqual(output, ["--codex", "/usr/local/bin/codex", "--agent-id", "codex-1",
                                  "--socket", "/tmp/collab-ai.sock", "--terminal", "--",
                                  "resume", "session with spaces", "-C", "/workspace/my repo"])

    def test_claude_uses_preconfigured_channel_and_preserves_arguments(self):
        output = self.shell('function /usr/local/bin/claude() { printf "%s\\n" "$@"; }\n'
                            'claude --resume "session with spaces"')
        self.assertEqual(output, ["--strict-mcp-config", "--mcp-config", "/etc/pairtask/claude-mcp.json",
                                  "--settings", "/etc/pairtask/claude-settings.json",
                                  "--dangerously-load-development-channels", "server:pairtask",
                                  "--resume", "session with spaces"])

    def test_dashboard_is_available_in_dev_and_control(self):
        for uid in (0, 1001):
            with self.subTest(uid=uid):
                output = self.shell('function /usr/local/bin/pairtask() { printf "%s\\n" "$@"; }\n'
                                    'dashboard --interval 3s', uid=uid)
                self.assertEqual(output, ["dashboard", "--interval", "3s"])

    def test_scripts_and_control_shells_do_not_get_agent_aliases(self):
        for uid, interactive in ((0, True), (1001, False)):
            with self.subTest(uid=uid, interactive=interactive):
                self.shell('! alias codex 2>/dev/null && ! alias claude 2>/dev/null',
                           uid=uid, interactive=interactive)
