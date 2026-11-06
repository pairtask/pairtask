"""Re-source the shipped launcher in a shell that has the historical alias."""

from pathlib import Path
import shlex
import subprocess
import tempfile
import unittest
from unittest.mock import patch

import test_claude_launcher_acceptance as launcher


class ClaudeAliasUpgradeAcceptance(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory(prefix="claude alias upgrade ")
        self.addCleanup(temporary.cleanup)
        wrapper = Path(temporary.name) / "historical-profile.sh"
        source = shlex.quote(str(launcher.ALIASES))
        wrapper.write_text("alias claude='/usr/local/bin/claude --old'\n"
                           f'. {source} || exit "$?"\n'
                           f'. {source} || exit "$?"\n')
        replacement = patch.object(launcher, "ALIASES", wrapper)
        replacement.start()
        self.addCleanup(replacement.stop)

    def test_historical_alias_is_replaced_before_subcommand_and_session_calls(self):
        for args, expected in ((["auth", "login"], ["auth", "login"]),
                               (["do x"], launcher.SESSION + ["do x"])):
            with self.subTest(args=args):
                try:
                    actual = launcher.forwarded(args)
                except subprocess.CalledProcessError as error:
                    self.fail(f"sourcing the actual launcher failed: {error.stderr}")
                self.assertEqual(actual, expected)

    def test_control_shell_can_source_with_an_alias_without_installing_a_function(self):
        try:
            result = launcher.shell("! declare -F claude >/dev/null && alias claude", uid=0)
        except subprocess.CalledProcessError as error:
            self.fail(f"control shell could not source the actual launcher: {error.stderr}")
        self.assertEqual(result.stdout, "alias claude='/usr/local/bin/claude --old'\n")


if __name__ == "__main__":
    unittest.main()
