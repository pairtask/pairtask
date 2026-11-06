"""A shared profile must parse before any interactive or Bash-only guard runs."""

import os
from itertools import product
import json
from pathlib import Path
import shlex
import shutil
import subprocess
import sys
import tempfile
import unittest

from test_claude_launcher_acceptance import SESSION

PROFILE = Path(__file__).resolve().parents[2] / "infra/image/pairtask-agent-aliases.sh"
DASH = shutil.which("dash")


def profile_shell(shell, interactive, command):
    env = {key: value for key, value in os.environ.items() if key not in ("ENV", "BASH_ENV")}
    args = [shell] + (["--noprofile", "--norc"] if Path(shell).name == "bash" else [])
    return subprocess.run(args + ["-ic" if interactive else "-c", command],
                          env=env, capture_output=True, text=True, timeout=10)


def source_profile(shell, interactive, uid, errexit=False):
    command = "set -e\n" if errexit else ""
    command += f'id() {{ printf "%s\\n" {uid}; }}\n. {shlex.quote(str(PROFILE))}'
    command += '\n' if errexit else ' || exit "$?"\n'
    if not interactive or uid != 1001:
        command += 'kind=$(command -V claude 2>/dev/null || :)\n'
        command += 'case "$kind" in *function*|*alias*) exit 17;; esac\n'
    command += 'printf "profile loaded\\n"\n'
    return profile_shell(shell, interactive, command)


def captured_profile_argv(shell, args):
    with tempfile.TemporaryDirectory(prefix="profile-capture-") as temporary:
        directory = Path(temporary)
        native = directory / "native-claude"
        native.write_text(f"#!{sys.executable}\nimport json,sys\nprint(json.dumps(sys.argv[1:]))\n")
        native.chmod(0o755)
        # Only replace the executable dependency; exercise the actual routing
        # body. Dash cannot define the absolute-path function stub Bash uses.
        profile = directory / "profile.sh"
        source = PROFILE.read_text()
        if "/usr/local/bin/claude" not in source:
            raise AssertionError("native target changed: review capture instrumentation")
        profile.write_text(source.replace("/usr/local/bin/claude", shlex.quote(str(native))))
        command = 'id() { printf "1001\\n"; }\nalias claude="legacy-native --old"\n'
        command += f'. {shlex.quote(str(profile))} || exit "$?"\n' * 2
        command += "claude " + shlex.join(args) + "\n"
        return profile_shell(shell, True, command)


class AgentProfilePosixAcceptance(unittest.TestCase):
    def test_shared_profile_is_valid_sh_syntax(self):
        result = subprocess.run(["/bin/sh", "-n", str(PROFILE)], capture_output=True, text=True, timeout=10)
        self.assertEqual(result.returncode, 0, result.stderr)

    @unittest.skipUnless(DASH, "dash is not installed")
    def test_dash_sources_profile_in_scripted_and_interactive_agent_and_control_shells(self):
        for interactive, uid in product((False, True), (0, 1001)):
            with self.subTest(interactive=interactive, uid=uid):
                result = source_profile(DASH, interactive, uid)
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertEqual(result.stdout, "profile loaded\n")

    def test_fresh_shells_with_errexit_continue_after_sourcing_the_profile(self):
        shells = [path for path in (shutil.which("bash"), DASH) if path]
        for shell, interactive, uid in product(shells, (False, True), (0, 1001)):
            with self.subTest(shell=shell, interactive=interactive, uid=uid):
                result = source_profile(shell, interactive, uid, errexit=True)
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertEqual(result.stdout, "profile loaded\n")

    def test_bash_and_dash_keep_actual_routing_argv_after_historical_alias(self):
        shells = [path for path in (shutil.which("bash"), DASH) if path]
        calls = ((["auth", "login"], ["auth", "login"]),
                 (["--resume", "x y", ""], SESSION + ["--resume", "x y", ""]))
        for shell, (args, expected) in product(shells, calls):
            with self.subTest(shell=shell, args=args):
                result = captured_profile_argv(shell, args)
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertEqual(json.loads(result.stdout), expected)

    def test_control_historical_alias_is_untouched_and_no_private_helper_is_installed(self):
        shells = [path for path in (shutil.which("bash"), DASH) if path]
        for shell in shells:
            with self.subTest(shell=shell):
                command = 'id() { printf "0\\n"; }\nalias claude="legacy-native --old"\n'
                command += f'. {shlex.quote(str(PROFILE))} || exit "$?"\n'
                command += 'alias claude\n! command -V pairtask_claude >/dev/null 2>&1\n'
                result = profile_shell(shell, True, command)
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertIn("legacy-native --old", result.stdout)


if __name__ == "__main__":
    unittest.main()
