"""Exercise the POSIX installer with local Git releases and isolated host tools."""

import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import unittest


INSTALLER = Path(__file__).resolve().parents[2] / "install.sh"
TAG_ONE = "workspace-v1.0.0"
TAG_TWO = "workspace-v1.0.1"
TAG_OLD = "workspace-v0.0.1"
HOST_PROGRAM = '''import json, os, sys
with open(os.environ["PAIRTASK_TEST_PROVISION_LOG"], "a") as stream:
    stream.write(json.dumps(sys.argv[1:]) + "\\n")
sys.exit(int(os.environ.get("PAIRTASK_TEST_PROVISION_EXIT", "0")))
'''


class HostInstallerTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.fixture = tempfile.TemporaryDirectory(prefix="pairtask release fixture ")
        cls.addClassCleanup(cls.fixture.cleanup)
        cls.repo = Path(cls.fixture.name) / "releases"
        cls.git = shutil.which("git")
        if cls.git is None:
            raise unittest.SkipTest("Git is required for local release fixtures")
        cls.repo.mkdir()
        cls.git_command("init", "-q")
        cls.git_command("config", "user.name", "Installer Test")
        cls.git_command("config", "user.email", "installer@example.invalid")
        (cls.repo / "README").write_text("old release\n")
        cls.commit_tag(TAG_OLD)
        (cls.repo / "scripts").mkdir()
        (cls.repo / "scripts/pairtask.py").write_text(HOST_PROGRAM)
        cls.commit_tag(TAG_ONE)
        (cls.repo / "README").write_text("new release\n")
        cls.commit_tag(TAG_TWO)

    @classmethod
    def git_command(cls, *args):
        return subprocess.run([cls.git, "-C", str(cls.repo), *args], check=True,
                              capture_output=True, text=True)

    @classmethod
    def commit_tag(cls, tag):
        cls.git_command("add", ".")
        cls.git_command("-c", "commit.gpgsign=false", "commit", "-qm", tag)
        cls.git_command("tag", tag)

    def setUp(self):
        temporary = tempfile.TemporaryDirectory(prefix="pairtask installer ")
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        self.home = self.root / "user home"
        self.home.mkdir()
        self.install_home = self.home / ".pairtask"
        self.bin = self.root / "tools"
        self.bin.mkdir()
        self.calls = self.root / "tool-calls.jsonl"
        self.provisions = self.root / "provision.jsonl"
        self.release = self.root / "release.json"
        self.release.write_text(json.dumps({"tag_name": TAG_TWO}))
        self.env = dict(os.environ, HOME=str(self.home), SHELL="/bin/bash",
                        PATH=str(self.bin), XDG_CONFIG_HOME=str(self.home / ".config"),
                        PAIRTASK_HOME=str(self.install_home),
                        PAIRTASK_RELEASE_URL=self.release.as_uri(),
                        PAIRTASK_GIT_URL=self.repo.as_uri(),
                        PAIRTASK_TEST_CALLS=str(self.calls),
                        PAIRTASK_TEST_PROVISION_LOG=str(self.provisions),
                        GIT_CONFIG_GLOBAL=os.devnull, GIT_CONFIG_NOSYSTEM="1")
        for name in ("SUDO_USER", "PAIRTASK_INSTALL_DRY_RUN", "PAIRTASK_REPO"):
            self.env.pop(name, None)
        for name in ("sh", "sed", "head", "cat", "mkdir", "chmod", "dirname", "grep", "rm"):
            target = shutil.which(name)
            if target:
                (self.bin / name).symlink_to(target)
        (self.bin / "python3").symlink_to(sys.executable)
        self.tool("uname", "print('Linux' if sys.argv[1] == '-s' else 'x86_64')")
        self.tool("id", "print(os.environ.get('PAIRTASK_TEST_UID', '1000'))")
        self.tool("git", "os.execv(%r, [%r] + sys.argv[1:])" % (self.git, self.git))
        self.tool("curl", """from urllib.parse import urlparse, unquote
url = sys.argv[-1]
if not url.startswith('file://'):
    sys.exit(91)  # No test may reach the network.
sys.stdout.write(open(unquote(urlparse(url).path)).read())
""")
        for name in ("incus", "colima", "brew"):
            self.tool(name, "sys.exit(0)")

    def tool(self, name, source):
        path = self.bin / name
        path.write_text("#!%s\nimport json, os, sys\n" % sys.executable
                        + "with open(os.environ['PAIRTASK_TEST_CALLS'], 'a') as stream:\n"
                        + "    stream.write(json.dumps([%r] + sys.argv[1:]) + '\\n')\n" % name
                        + source + "\n")
        path.chmod(0o755)

    def install(self, *args, **overrides):
        return subprocess.run(["/bin/sh", str(INSTALLER), *args],
                              env=dict(self.env, **overrides), cwd=self.root,
                              input="", capture_output=True, text=True, timeout=30,
                              start_new_session=True)

    def assert_success(self, result):
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)

    def records(self, path):
        return [json.loads(line) for line in path.read_text().splitlines()] if path.exists() else []

    def installed_revision(self):
        return subprocess.check_output([self.git, "-C", str(self.install_home / "host"),
                                        "rev-parse", "HEAD"], text=True).strip()

    def test_posix_syntax_and_argument_errors(self):
        syntax = subprocess.run(["/bin/sh", "-n", str(INSTALLER)], capture_output=True, text=True)
        self.assert_success(syntax)
        for flags in (("--bogus",), ("--version",), ("--version=",),
                      ("--version", "--upload-pack=oops"), ("--version", "main")):
            with self.subTest(flags=flags):
                self.assertEqual(self.install(*flags).returncode, 2)
        self.assertFalse(self.install_home.exists())

    def test_dry_run_has_no_network_or_install_writes(self):
        result = self.install(PAIRTASK_INSTALL_DRY_RUN="1")
        self.assert_success(result)
        self.assertIn("plan:", result.stdout)
        self.assertFalse(self.install_home.exists())
        self.assertFalse((self.home / ".bashrc").exists())
        self.assertFalse(self.provisions.exists())
        calls = self.records(self.calls)
        self.assertFalse([call for call in calls if call[0] in {"git", "curl", "brew", "incus", "colima"}], calls)
        (self.bin / "incus").unlink()
        self.assert_success(self.install(PAIRTASK_INSTALL_DRY_RUN="1"))
        self.assertFalse(self.install_home.exists())
        self.assertFalse([call for call in self.records(self.calls)
                          if call[0] in {"git", "curl", "brew", "incus", "colima"}])

    def test_latest_release_is_pinned_for_provisioning(self):
        result = self.install("--no-modify-path")
        self.assert_success(result)
        self.assertEqual(self.records(self.provisions), [["install", "--release", TAG_TWO]])
        expected = self.git_command("rev-parse", TAG_TWO).stdout.strip()
        self.assertEqual(self.installed_revision(), expected)
        self.assert_success(self.install("--version", TAG_ONE, "--no-modify-path"))
        self.assertEqual(self.records(self.provisions)[-1], ["upgrade", "--release", TAG_ONE])

    def test_reinstall_preserves_ssh_images_and_existing_shell_configuration(self):
        rc = self.home / ".bashrc"
        rc.write_text("# existing shell configuration\n")
        self.assert_success(self.install("--version", TAG_ONE, "--no-provision"))
        host = self.install_home / "host"
        sentinels = {"infra/incus/ssh/id_ed25519": b"private-key-sentinel\n",
                     "dist/images/old/image": b"verified-image-sentinel\n",
                     "infra/incus/terraform.tfstate": b"state-sentinel\n"}
        for name, value in sentinels.items():
            path = host / name
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_bytes(value)
        first_rc = rc.read_bytes()
        self.assert_success(self.install("--version", TAG_TWO, "--no-provision"))
        self.assertEqual(rc.read_bytes(), first_rc)
        for name, value in sentinels.items():
            self.assertEqual((host / name).read_bytes(), value)
        launcher = self.install_home / "bin/pairtask"
        result = subprocess.run([str(launcher), "shell", "a path;$(literal)", "--dry-run"],
                                env=self.env, capture_output=True, text=True, timeout=10)
        self.assert_success(result)
        self.assertEqual(self.records(self.provisions), [["shell", "a path;$(literal)", "--dry-run"]])

    def test_failed_upgrade_keeps_installed_revision_and_launcher(self):
        self.assert_success(self.install("--version", TAG_TWO, "--no-provision", "--no-modify-path"))
        revision = self.installed_revision()
        launcher = (self.install_home / "bin/pairtask").read_bytes()
        for tag in (TAG_OLD, "workspace-v9.9.9"):
            with self.subTest(tag=tag):
                result = self.install("--version", tag, "--no-provision", "--no-modify-path")
                self.assertEqual(result.returncode, 3, result.stdout + result.stderr)
                self.assertEqual(self.installed_revision(), revision)
                self.assertEqual((self.install_home / "bin/pairtask").read_bytes(), launcher)
        changed = self.install_home / "host/README"
        changed.write_text("operator edit\n")
        result = self.install("--version", TAG_ONE, "--no-provision", "--no-modify-path")
        self.assertEqual(result.returncode, 3, result.stdout + result.stderr)
        self.assertEqual(changed.read_text(), "operator edit\n")
        self.assertEqual(self.installed_revision(), revision)

    def test_provision_failure_is_propagated_without_success_message(self):
        result = self.install("--version", TAG_ONE, "--no-modify-path", PAIRTASK_TEST_PROVISION_EXIT="23")
        self.assertEqual(result.returncode, 23, result.stdout + result.stderr)
        self.assertNotIn("next: pairtask shell", result.stdout)

    def test_install_path_is_literal_in_launcher_and_shell_rc(self):
        self.install_home = self.home / 'tools $literal "quoted"'
        self.env["PAIRTASK_HOME"] = str(self.install_home)
        self.env["literal"] = "must-not-expand"
        self.assert_success(self.install("--version", TAG_ONE, "--no-provision"))
        result = subprocess.run([str(self.install_home / "bin/pairtask"), "status"],
                                env=self.env, capture_output=True, text=True, timeout=10)
        self.assert_success(result)
        self.assertEqual(self.records(self.provisions), [["status"]])
        shell = subprocess.run(["/bin/sh", "-c", '. "$1"; printf "%s" "$PATH"',
                                "test-shell", str(self.home / ".bashrc")],
                               env=self.env, capture_output=True, text=True, timeout=10)
        self.assert_success(shell)
        self.assertEqual(shell.stdout, str(self.install_home / "bin") + ":" + self.env["PATH"])

    def test_root_and_sudo_refused_before_install_changes(self):
        for overrides in ({"PAIRTASK_TEST_UID": "0"}, {"SUDO_USER": "someone"}):
            with self.subTest(overrides=overrides):
                self.assertEqual(self.install("--no-provision", **overrides).returncode, 1)
        self.assertFalse(self.install_home.exists())

    def test_linux_missing_prerequisite_stops_before_fetch(self):
        (self.bin / "incus").unlink()
        result = self.install("--no-provision")
        self.assertEqual(result.returncode, 1, result.stdout + result.stderr)
        self.assertIn("Incus", result.stdout + result.stderr)
        self.assertFalse(self.install_home.exists())
        self.assertFalse([call for call in self.records(self.calls) if call[0] in {"git", "curl"}])

    def test_mac_brew_requires_consent_and_yes_installs_prerequisites(self):
        self.tool("uname", "print('Darwin' if sys.argv[1] == '-s' else 'arm64')")
        missing = self.bin / "incus"
        saved = self.bin / "incus.saved"
        missing.rename(saved)
        self.tool("brew", "os.rename(%r, %r)" % (str(saved), str(missing)))
        denied = self.install("--version", TAG_ONE, "--no-provision", "--no-modify-path")
        self.assertEqual(denied.returncode, 1, denied.stdout + denied.stderr)
        self.assertFalse(self.install_home.exists())
        self.assertFalse([call for call in self.records(self.calls) if call[0] == "brew"])
        accepted = self.install("--version", TAG_ONE, "--no-provision", "--no-modify-path", "--yes")
        self.assert_success(accepted)
        self.assertEqual([call for call in self.records(self.calls) if call[0] == "brew"],
                         [["brew", "install", "colima", "incus", "python", "git"]])


if __name__ == "__main__":
    unittest.main()
