"""Build caches use the existing workspace volume without changing projects (#400)."""

from contextlib import contextmanager
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
CACHE_SUBDIRS = ("go-build", "go-mod", "npm", "pip", "uv")


class AgentCacheVolumeAcceptance(unittest.TestCase):
    @contextmanager
    def setup_environment(self, cache, env_file, *, mounted=True):
        account = types.SimpleNamespace(pw_uid=os.getuid(), pw_gid=os.getgid())
        roots = mock.Mock()
        if not mounted:
            roots.side_effect = ValueError("Expected an Incus volume at /workspace")
        globals_ = SETUP["initialize"].__globals__
        with mock.patch.dict(globals_, {"CACHE": cache, "CACHE_ENV": env_file,
                                       "own_root": roots, "seed_home": mock.Mock()}), \
                mock.patch.object(SETUP["pwd"], "getpwnam", return_value=account), \
                mock.patch.object(os, "chown") as chown:
            yield roots, chown, account

    def test_setup_creates_private_cache_and_service_env(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            cache = root / ".cache"
            env_file = root / "run/pairtask/cache.env"
            with self.setup_environment(cache, env_file) as (roots, chown, account):
                SETUP["initialize"]()
                roots.assert_any_call(Path("/workspace"), 0o750, account)
                chown.assert_called_once_with(cache, account.pw_uid, account.pw_gid, follow_symlinks=False)
            self.assertEqual(cache.stat().st_mode & 0o777, 0o700)
            self.assertEqual(env_file.read_text(), "".join(
                f"{name}={cache / sub}\n" for name, sub in zip(CACHE_KEYS, CACHE_SUBDIRS)))
            self.assertEqual(env_file.stat().st_mode & 0o777, 0o644)

    def test_existing_cache_and_projects_are_preserved_without_recursing(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            cache = root / ".cache"
            cache.mkdir(mode=0o755)
            child = cache / "preserve"
            child.write_text("cached artifact")
            child.chmod(0o644)
            project = root / "project.txt"
            project.write_text("project data")
            with self.setup_environment(cache, root / "cache.env") as (_, chown, account):
                SETUP["initialize"]()
                chown.assert_called_once_with(cache, account.pw_uid, account.pw_gid, follow_symlinks=False)
            self.assertEqual(cache.stat().st_mode & 0o777, 0o700)
            self.assertEqual(child.read_text(), "cached artifact")
            self.assertEqual(child.stat().st_mode & 0o777, 0o644)
            self.assertEqual(project.read_text(), "project data")

    def test_cache_symlink_is_refused_without_touching_its_target(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            target = root / "target"
            target.mkdir(mode=0o755)
            cache = root / ".cache"
            cache.symlink_to(target, target_is_directory=True)
            env_file = root / "cache.env"
            with self.setup_environment(cache, env_file) as (_, chown, _):
                with self.assertRaisesRegex(ValueError, "Expected a private directory"):
                    SETUP["initialize"]()
                chown.assert_not_called()
            self.assertTrue(cache.is_symlink())
            self.assertEqual(target.stat().st_mode & 0o777, 0o755)
            self.assertFalse(env_file.exists())

    def test_missing_workspace_mount_stops_setup_before_cache_is_created(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            cache, env_file = root / ".cache", root / "cache.env"
            with self.setup_environment(cache, env_file, mounted=False) as (_, chown, _):
                with self.assertRaisesRegex(ValueError, "Expected an Incus volume"):
                    SETUP["initialize"]()
                chown.assert_not_called()
            self.assertFalse(cache.exists())
            self.assertFalse(env_file.exists())

    def test_shell_uses_mounted_workspace_and_keeps_presets(self):
        source = (ROOT / "infra/image/pairtask-cache.sh").read_text()
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            workspace = root / "workspace"
            workspace.mkdir()
            cache = workspace / ".cache"
            cache.mkdir()
            script = root / "cache.sh"
            script.write_text(source.replace("/workspace", str(workspace)))
            self.assertEqual(self.source_profile(script, workspace), [""] * len(CACHE_KEYS))
            self.assertEqual(self.source_profile(script, workspace, mounted=True),
                             [str(cache / sub) for sub in CACHE_SUBDIRS])
            self.assertEqual(self.source_profile(script, workspace, mounted=True,
                                                 GOCACHE="/chosen/go")[0], "/chosen/go")

    def source_profile(self, script, workspace, *, mounted=False, **overrides):
        env = {key: value for key, value in os.environ.items() if key not in CACHE_KEYS}
        env.update(overrides)
        # Require the workspace mount, not the .cache child, without making a real mount.
        body = ('mountpoint() { [ "$1" = "-q" ] && [ "$2" = '
                + shlex.quote(str(workspace)) + ' ] && return '
                + str(0 if mounted else 1) + '; return 1; }\n')
        body += ". " + shlex.quote(str(script)) + "\n" + "\n".join(
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
