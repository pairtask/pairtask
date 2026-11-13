import importlib.util
import os
from pathlib import Path
import shlex
import subprocess
import tempfile
import unittest


ROOT = Path(__file__).resolve().parents[2]
HOME_CHECK = ROOT / "infra/image/pairtask-home-check.sh"
STARSHIP = ROOT / "infra/image/pairtask-starship.sh"


def run_shell(source, setup, body, **options):
    uid = options.get("uid", 1001)
    interactive = options.get("interactive", True)
    env = options.get("env")
    command = (f'id() {{ printf "%s\\n" {uid}; }}\n' + setup +
               f'. {shlex.quote(str(source))}\n' + body)
    return subprocess.run(["bash", "--noprofile", "--norc", "-ic" if interactive else "-c", command],
                          capture_output=True, text=True, env=env, check=True)


class HomeCapacityShellAcceptanceTests(unittest.TestCase):
    def test_home_check_runs_once_for_an_interactive_agent_and_never_fails_shell(self):
        again = f'. {shlex.quote(str(HOME_CHECK))}\nprintf "calls=%s" "$calls"'
        setup = ('calls=0\n'
                 'function /usr/local/bin/pairtask() { '
                 'test "$*" = "home-check" || return 11; '
                 'calls=$((calls + 1)); return 9; }\n')
        result = run_shell(HOME_CHECK, setup, again)
        self.assertEqual(result.stdout, "calls=1")

    def test_home_check_skips_noninteractive_and_other_users(self):
        setup = 'calls=0\nfunction /usr/local/bin/pairtask() { calls=$((calls + 1)); }\n'
        for uid, interactive in ((1001, False), (0, True)):
            with self.subTest(uid=uid, interactive=interactive):
                result = run_shell(HOME_CHECK, setup, 'printf "calls=%s" "$calls"', uid=uid, interactive=interactive)
                self.assertEqual(result.stdout, "calls=0")

    def test_starship_cache_is_outside_home_before_prompt_initializes(self):
        with tempfile.TemporaryDirectory() as directory:
            home = Path(directory) / "home"
            runtime = Path(directory) / "runtime"
            home.mkdir()
            runtime.mkdir()
            env = dict(os.environ, HOME=str(home), XDG_RUNTIME_DIR=str(runtime))
            setup = ('function /usr/local/bin/starship() { '
                     'printf "cache=%s\\n" "$STARSHIP_CACHE" >&2; '
                     'printf "PROMPT_COMMAND=ready"; }\n')
            result = run_shell(STARSHIP, setup, 'printf "%s" "$PROMPT_COMMAND"', env=env)
            self.assertEqual(result.stdout, "ready")
            self.assertIn(f"cache={runtime}/starship-1001", result.stderr)
            self.assertNotIn(f"cache={home}", result.stderr)

    def test_image_installs_and_bundles_home_check(self):
        install = (ROOT / "infra/image/install.sh").read_text()
        self.assertIn("install -m 0644 infra/image/pairtask-home-check.sh /etc/profile.d/pairtask-home-check.sh", install)
        self.assertIn(". /etc/profile.d/pairtask-home-check.sh", install)
        source = ROOT / "scripts/sandbox_source.py"
        spec = importlib.util.spec_from_file_location("sandbox_source", source)
        module = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(module)
        self.assertIn("infra/image/pairtask-home-check.sh", module.RECIPE_PATHS)


if __name__ == "__main__":
    unittest.main()
