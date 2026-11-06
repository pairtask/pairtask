"""Exercise launcher argv in a shell; never start a model or real CLI session."""

import json
from pathlib import Path
import shlex
import subprocess
import unittest

ROOT = Path(__file__).resolve().parents[2]
ALIASES = ROOT / "infra/image/pairtask-agent-aliases.sh"
SESSION = ["--strict-mcp-config", "--mcp-config", "/etc/pairtask/claude-mcp.json",
           "--settings", "/etc/pairtask/claude-settings.json",
           "--dangerously-load-development-channels", "server:pairtask"]
# Captured from Claude Code 2.1.283 --help; image proof must confirm its binary.
SUBCOMMANDS = ("agents", "attach", "auth", "auto-mode", "doctor", "gateway", "import", "install",
               "logs", "mcp", "plugin", "plugins", "project", "respawn", "rm", "setup-token",
               "stop", "kill", "ultrareview", "update", "upgrade")


def shell(command, uid=1001, interactive=True):
    setup = f'id() {{ printf "%s\\n" {uid}; }}\n. {shlex.quote(str(ALIASES))}\n'
    return subprocess.run(["bash", "--noprofile", "--norc", "-ic" if interactive else "-c",
                           setup + command], check=True, capture_output=True, text=True, timeout=10)


def forwarded(args):
    capture = "import json,sys; print(json.dumps(sys.argv[1:]))"
    stub = f'function /usr/local/bin/claude() {{ python3 -c {shlex.quote(capture)} "$@"; }}\n'
    # A launcher may use `command` to bypass another function. Keep that
    # absolute-binary call inside the capture stub too, never the real CLI.
    stub += 'command() { if [ "$1" = /usr/local/bin/claude ]; then shift; /usr/local/bin/claude "$@"; else builtin command "$@"; fi; }\n'
    return json.loads(shell(stub + "claude " + shlex.join(args) + "\n").stdout)


class ClaudeLauncherAcceptance(unittest.TestCase):
    def test_all_pinned_cli_subcommands_pass_through_unchanged(self):
        for name in SUBCOMMANDS:
            with self.subTest(subcommand=name):
                args = [name, "argument with spaces", "", "--flag"]
                self.assertEqual(forwarded(args), args)

    def test_auth_login_is_not_given_channel_or_session_flags(self):
        self.assertEqual(forwarded(["auth", "login"]), ["auth", "login"])

    def test_sessions_get_settings_and_preserve_every_user_argument(self):
        for args in ([], ["do x"], [""], ["new-command", "x y", ""], ["-p", "do x"],
                     ["--print", "do x"], ["--resume", "x y"], ["--continue"],
                     ["--settings", "/tmp/user settings.json", "--mcp-config", "/tmp/user mcp.json", "do x"]):
            with self.subTest(args=args):
                self.assertEqual(forwarded(args), SESSION + args)

    def test_control_and_noninteractive_shells_do_not_install_a_claude_wrapper(self):
        for uid, interactive in ((0, True), (1001, False)):
            with self.subTest(uid=uid, interactive=interactive):
                shell("! alias claude 2>/dev/null && ! declare -F claude >/dev/null", uid, interactive)


if __name__ == "__main__":
    unittest.main()
