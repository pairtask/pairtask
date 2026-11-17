"""The optional build-cache volume keeps tool caches off agent home (#400)."""

import os
from pathlib import Path
import runpy
import shlex
import subprocess
import tempfile
import types
import unittest
from unittest import mock


ROOT = Path(__file__).resolve().parents[2]
SETUP = runpy.run_path(str(ROOT / "infra/image/pairtask-workspace-setup"))
CACHE_KEYS = ("GOCACHE", "GOMODCACHE", "npm_config_cache", "PIP_CACHE_DIR", "UV_CACHE_DIR")


class AgentCacheVolumeAcceptance(unittest.TestCase):
    def test_mounted_cache_is_owned_without_recursing_and_service_env_is_written(self):
        with tempfile.TemporaryDirectory() as temporary:
            cache = Path(temporary) / "cache"
            cache.mkdir(mode=0o755)
            child = cache / "preserve"
            child.write_text("keep")
            child.chmod(0o644)
            account = types.SimpleNamespace(pw_uid=os.getuid(), pw_gid=os.getgid())
            with mock.patch.object(Path, "is_mount", return_value=True), mock.patch.object(os, "chown") as chown:
                SETUP["own_root"](cache, 0o700, account)
            chown.assert_called_once_with(cache, account.pw_uid, account.pw_gid)
            self.assertEqual(cache.stat().st_mode & 0o777, 0o700)
            self.assertEqual(child.read_text(), "keep")
            self.assertEqual(child.stat().st_mode & 0o777, 0o644)

            env_file = Path(temporary) / "run/pairtask/cache.env"
            SETUP["write_cache_env"](cache, env_file)
            self.assertEqual(env_file.read_text(), "".join(
                f"{name}={cache / sub}\n" for name, sub in (
                    ("GOCACHE", "go-build"), ("GOMODCACHE", "go-mod"),
                    ("npm_config_cache", "npm"), ("PIP_CACHE_DIR", "pip"), ("UV_CACHE_DIR", "uv"))))
            self.assertEqual(env_file.stat().st_mode & 0o777, 0o644)

    def test_missing_cache_removes_stale_env_and_symlink_is_refused(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            cache = root / "cache"
            env_file = root / "run/pairtask/cache.env"
            env_file.parent.mkdir(parents=True)
            env_file.write_text("GOCACHE=/stale\n")
            globals_ = SETUP["initialize"].__globals__
            with mock.patch.dict(globals_, {"CACHE": cache, "CACHE_ENV": env_file,
                                            "own_root": mock.Mock(), "seed_home": mock.Mock()}), \
                    mock.patch.object(SETUP["pwd"], "getpwnam", return_value=types.SimpleNamespace(pw_uid=1, pw_gid=1)):
                SETUP["initialize"]()
                globals_["own_root"].assert_any_call(Path("/home/agent"), 0o700, mock.ANY)
                self.assertNotIn(cache, [call.args[0] for call in globals_["own_root"].call_args_list])
            self.assertFalse(env_file.exists())

            target = root / "other"
            target.mkdir()
            cache.symlink_to(target, target_is_directory=True)
            with self.assertRaisesRegex(ValueError, "Expected an Incus volume"):
                SETUP["own_root"](cache, 0o700, types.SimpleNamespace(pw_uid=1, pw_gid=1))
            self.assertTrue(cache.is_symlink())

    def test_unmounted_cache_directory_after_old_rollout_is_not_used(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            cache = root / "cache"
            cache.mkdir()  # The old checkout removed the volume, not its mountpoint.
            env_file = root / "run/pairtask/cache.env"
            env_file.parent.mkdir(parents=True)
            env_file.write_text("GOCACHE=/stale\n")
            globals_ = SETUP["initialize"].__globals__
            with mock.patch.dict(globals_, {"CACHE": cache, "CACHE_ENV": env_file,
                                            "own_root": mock.Mock(), "seed_home": mock.Mock()}), \
                    mock.patch.object(SETUP["pwd"], "getpwnam", return_value=types.SimpleNamespace(pw_uid=1, pw_gid=1)), \
                    mock.patch.object(Path, "is_mount", return_value=False):
                SETUP["initialize"]()
                self.assertNotIn(cache, [call.args[0] for call in globals_["own_root"].call_args_list])
            self.assertFalse(env_file.exists())
            self.assertTrue(cache.is_dir())

    def test_shell_uses_cache_only_when_present_and_keeps_presets(self):
        source = (ROOT / "infra/image/pairtask-cache.sh").read_text()
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            cache = root / "cache"
            script = root / "cache.sh"
            script.write_text(source.replace("/var/cache/collab-ai-agent", str(cache)))
            self.assertEqual(self.source_profile(script), [""] * len(CACHE_KEYS))
            cache.mkdir()
            # An old checkout can leave this directory after unmounting the volume.
            self.assertEqual(self.source_profile(script), [""] * len(CACHE_KEYS))
            self.assertEqual(self.source_profile(script, mounted=True), [str(cache / sub) for sub in
                             ("go-build", "go-mod", "npm", "pip", "uv")])
            self.assertEqual(self.source_profile(script, mounted=True, GOCACHE="/chosen/go")[0], "/chosen/go")

    def source_profile(self, script, *, mounted=False, **overrides):
        env = {key: value for key, value in os.environ.items() if key not in CACHE_KEYS}
        env.update(overrides)
        # Simulate mountpoint(1) without requiring a real mount in the test.
        body = f"mountpoint() {{ return {0 if mounted else 1}; }}\n" + ". " + shlex.quote(str(script)) + "\n" + "\n".join(
            f'printf "%s\\n" "${key}"' for key in CACHE_KEYS)
        result = subprocess.run(["bash", "--noprofile", "--norc", "-c", body],
                                check=True, text=True, capture_output=True, env=env)
        return result.stdout.splitlines()

    def test_executor_reads_boot_env_and_shells_source_cache_profile(self):
        service = (ROOT / "infra/image/pairtask-dev-executor@.service").read_text()
        install = (ROOT / "infra/image/install.sh").read_text()
        self.assertIn("EnvironmentFile=-/run/pairtask/cache.env", service)
        self.assertIn("infra/image/pairtask-cache.sh /etc/profile.d/pairtask-cache.sh", install)
        self.assertIn(". /etc/profile.d/pairtask-cache.sh", install)


if __name__ == "__main__":
    unittest.main()
