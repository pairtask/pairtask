"""Acceptance for installed pairing setup and restart safety (#178)."""

import errno
import os
from pathlib import Path
import runpy
import socket
import stat
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import MagicMock, patch


ROOT = Path(__file__).resolve().parents[2]
SETUP = runpy.run_path(str(ROOT / "infra/image/pairtask-pairing-setup"))


class PairingRouteSetupAcceptanceTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory(dir="/tmp", prefix="route-setup-")
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        self.path = self.root / "endpoint.sock"

    def stale_socket(self):
        with socket.socket(socket.AF_UNIX) as listener:
            listener.bind(str(self.path))

    def test_roles_use_mounts_and_require_both_route_volumes(self):
        state, ingress, egress = (self.root / name for name in ("state", "ingress", "egress"))
        mounted = {state, ingress, egress}
        check = lambda role: SETUP["check_role"](role, state, ingress, egress, lambda path: Path(path) in mounted)
        check("secured")
        with self.assertRaises(ValueError):
            check("connector")
        mounted.remove(state)
        state.mkdir()  # An ordinary directory must not turn connector into secured.
        check("connector")
        with self.assertRaises(ValueError):
            check("secured")
        for missing in (ingress, egress):
            mounted.remove(missing)
            with self.assertRaises(ValueError):
                check("connector")
            mounted.add(missing)

    def test_prepare_preserves_contents_and_sets_only_directory_permissions(self):
        private = self.root / "keep"
        private.write_bytes(b"unchanged")
        private.chmod(0o600)
        before = private.stat()
        SETUP["prepare_shared_dir"](self.root, os.getuid(), os.getgid())
        self.assertEqual(stat.S_IMODE(self.root.stat().st_mode), 0o750)
        self.assertEqual(private.read_bytes(), b"unchanged")
        self.assertEqual(private.stat().st_ino, before.st_ino)
        self.assertEqual(stat.S_IMODE(private.stat().st_mode), 0o600)

    def test_prepare_refuses_symlink_or_file(self):
        real = self.root / "real"
        real.mkdir()
        link = self.root / "link"
        link.symlink_to(real)
        file = self.root / "file"
        file.write_text("keep")
        for path in (link, file):
            with self.subTest(path=path), self.assertRaises(ValueError):
                SETUP["prepare_shared_dir"](path, os.getuid(), os.getgid())
        self.assertTrue(link.is_symlink())
        self.assertEqual(file.read_text(), "keep")

    def test_active_socket_is_kept_and_stale_socket_removed(self):
        with socket.socket(socket.AF_UNIX) as listener:
            listener.bind(str(self.path))
            listener.listen(1)
            before = self.path.lstat()
            with self.assertRaisesRegex(ValueError, "active"):
                SETUP["remove_stale_socket"](self.path, os.getuid())
            self.assertEqual(self.path.lstat().st_ino, before.st_ino)
        SETUP["remove_stale_socket"](self.path, os.getuid())
        self.assertFalse(self.path.exists())
        SETUP["remove_stale_socket"](self.path, os.getuid())

    def test_files_symlinks_and_foreign_socket_are_kept(self):
        self.path.write_text("keep")
        with self.assertRaises(ValueError):
            SETUP["remove_stale_socket"](self.path, os.getuid())
        self.assertEqual(self.path.read_text(), "keep")
        self.path.unlink()
        self.path.symlink_to("missing")
        with self.assertRaises(ValueError):
            SETUP["remove_stale_socket"](self.path, os.getuid())
        self.assertTrue(self.path.is_symlink())
        self.path.unlink()
        self.stale_socket()
        with self.assertRaises(ValueError):
            SETUP["remove_stale_socket"](self.path, os.getuid() + 1)
        self.assertTrue(stat.S_ISSOCK(self.path.lstat().st_mode))

    def mock_probe(self, failure):
        probe = MagicMock()
        probe.__enter__.return_value = probe
        probe.connect.side_effect = failure
        return patch.object(SETUP["socket"], "socket", return_value=probe)

    def test_ambiguous_connect_errors_never_mean_stale(self):
        self.stale_socket()
        before = self.path.lstat()
        for failure in (PermissionError(errno.EACCES, "denied"), TimeoutError("slow")):
            with self.subTest(error=type(failure)), self.mock_probe(failure):
                with self.assertRaises((ValueError, OSError)):
                    SETUP["remove_stale_socket"](self.path, os.getuid())
                self.assertEqual(self.path.lstat().st_ino, before.st_ino)

    def test_socket_replaced_during_probe_is_never_removed(self):
        self.stale_socket()

        def replace_then_refuse(_):
            self.path.unlink()
            self.path.write_text("replacement")
            raise ConnectionRefusedError(errno.ECONNREFUSED, "stale")

        with self.mock_probe(replace_then_refuse):
            with self.assertRaises((ValueError, OSError)):
                SETUP["remove_stale_socket"](self.path, os.getuid())
        self.assertEqual(self.path.read_text(), "replacement")

    def test_endpoint_cleanup_is_noop_when_route_mount_is_absent(self):
        self.path.write_text("unrelated")
        SETUP["clean_endpoint"](self.path, mounted=lambda _: False)
        self.assertEqual(self.path.read_text(), "unrelated")

    def env_values(self):
        return {"PAIRTASK_PAIRING_SOCKET": "/mnt/pairtask-pairing/pairing.sock",
                "PAIRTASK_PAIRING_SOCKET_GROUP": "collab-peer",
                "PAIRTASK_PAIRING_EGRESS": "socket:/mnt/pairtask-egress/egress.sock"}

    def write_env(self, path, values):
        with patch.object(SETUP["os"], "chown") as chown, patch.object(SETUP["os"], "fchown") as fchown:
            SETUP["write_env"](path, os.getgid(), values)
        calls = chown.call_args_list + fchown.call_args_list
        self.assertTrue(calls, "environment ownership was not set")
        for call in calls:
            self.assertEqual(call.args[-2:], (0, os.getgid()))

    def test_env_has_fixed_values_private_mode_and_root_owner(self):
        path = self.root / "pairing.env"
        self.write_env(path, self.env_values())
        self.assertEqual(stat.S_IMODE(path.stat().st_mode), 0o640)
        for key, value in self.env_values().items():
            self.assertIn(key + "=" + value, path.read_text())
        self.assertEqual(len(path.read_text().splitlines()), 3)

    def test_invalid_env_input_preserves_existing_content(self):
        path = self.root / "pairing.env"
        path.write_text("original")
        for key, value in (("UNRELATED", "x"), ("PAIRTASK_PAIRING_SOCKET", "bad\nNEW=x"),
                           ("PAIRTASK_PAIRING_EGRESS", "bad\0value")):
            values = self.env_values()
            values[key] = value
            with self.subTest(key=key, value=value), self.assertRaises(ValueError):
                self.write_env(path, values)
            self.assertEqual(path.read_text(), "original")

    def test_env_symlink_is_never_followed_or_cleared(self):
        target = self.root / "private"
        target.write_text("keep")
        path = self.root / "pairing.env"
        path.symlink_to(target)
        with self.assertRaises(ValueError):
            self.write_env(path, self.env_values())
        with self.assertRaises(ValueError):
            SETUP["clear_env"](path)
        self.assertTrue(path.is_symlink())
        self.assertEqual(target.read_text(), "keep")

    def test_failed_atomic_env_replace_keeps_old_file(self):
        path = self.root / "pairing.env"
        path.write_text("original")
        with patch.object(SETUP["os"], "replace", side_effect=OSError("injected rename failure")):
            with self.assertRaises((ValueError, OSError)):
                self.write_env(path, self.env_values())
        self.assertEqual(path.read_text(), "original")
        self.assertEqual(set(self.root.iterdir()), {path})

    def test_disable_removes_only_regular_env(self):
        path = self.root / "pairing.env"
        path.write_text("configured")
        SETUP["disable"](env=path)
        self.assertFalse(path.exists())
        SETUP["disable"](env=path)
        path.mkdir()
        with self.assertRaises(ValueError):
            SETUP["clear_env"](path)
        self.assertTrue(path.is_dir())

    def test_fixed_service_identities_are_checked(self):
        group = SimpleNamespace(gr_gid=1050)
        peer = SimpleNamespace(pw_uid=1052, pw_gid=1050)
        with patch.object(SETUP["grp"], "getgrnam", return_value=group), patch.object(SETUP["pwd"], "getpwnam", return_value=peer):
            SETUP["require_ids"]()
            group.gr_gid = 1053
            with self.assertRaises(ValueError):
                SETUP["require_ids"]()
            group.gr_gid, peer.pw_uid = 1050, 1053
            with self.assertRaises(ValueError):
                SETUP["require_ids"]()
            peer.pw_uid, peer.pw_gid = 1052, 1053
            with self.assertRaises(ValueError):
                SETUP["require_ids"]()

    def test_secured_refuses_unsafe_environment_parent(self):
        parent = self.root / "settings"
        parent.mkdir(mode=0o777)
        parent.chmod(0o777)
        target = self.root / "target"
        target.mkdir()
        alias = self.root / "alias"
        alias.symlink_to(target)
        namespace = SETUP["secured"].__globals__
        mocks = {name: MagicMock() for name in ("check_role", "require_ids", "prepare_shared_dir")}
        broker = SimpleNamespace(pw_uid=1002, pw_gid=1002)
        with patch.dict(namespace, mocks), patch.object(SETUP["pwd"], "getpwnam", return_value=broker):
            with patch.object(SETUP["os"], "chown"):
                for directory in (parent, alias):
                    with self.subTest(directory=directory), self.assertRaises(ValueError):
                        SETUP["secured"](env=directory / "pairing.env")
                    self.assertFalse((directory / "pairing.env").exists())

    def test_root_preparation_does_not_remove_daemon_sockets(self):
        namespace = SETUP["connector"].__globals__
        names = ("check_role", "require_ids", "prepare_shared_dir", "remove_stale_socket", "write_env")
        mocks = {name: MagicMock() for name in names}
        broker = SimpleNamespace(pw_uid=1002, pw_gid=1002)
        with patch.dict(namespace, mocks), patch.object(SETUP["pwd"], "getpwnam", return_value=broker):
            SETUP["connector"](ingress=self.root / "in", egress=self.root / "out")
            mocks["remove_stale_socket"].assert_not_called()
            mocks["prepare_shared_dir"].assert_called_once_with(self.root / "out", 1052, 1050)

    def test_environment_parent_checks_owner_and_write_bits(self):
        parent = self.root / "settings"
        SETUP["private_parent"](parent, uid=os.getuid())
        self.assertEqual(stat.S_IMODE(parent.stat().st_mode), 0o755)
        with self.assertRaises(ValueError):
            SETUP["private_parent"](parent, uid=os.getuid() + 1)
        for mode in (0o775, 0o757):
            parent.chmod(mode)
            with self.subTest(mode=oct(mode)), self.assertRaises(ValueError):
                SETUP["private_parent"](parent, uid=os.getuid())


if __name__ == "__main__":
    unittest.main()
