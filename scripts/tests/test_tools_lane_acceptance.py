"""The tools lane's checks, offline: publisher checksums, verified downloads,
unpacking, the Claude subcommand list and the Codex App Server handshake run
against fixtures and fake clients, never a publisher or a real CLI."""

import hashlib
import io
import json
import os
from pathlib import Path
import stat
import subprocess
import sys
import tarfile
import tempfile
import textwrap
import time
import unittest
from unittest import mock
import zipfile

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / "scripts"))
import sandbox_versions  # noqa: E402
import tools_lane  # noqa: E402

HELP = """Usage: claude [options] [command] [prompt]

Claude Code - starts an interactive session by default.

Options:
  -d, --debug [filter]                  Enable debug mode
  -h, --help                            Display help for command

Commands:
  agents [options]                      Manage background agents
  auth                                  Manage authentication
  plugin|plugins                        Manage Claude Code plugins
  purge [options] [path]                Delete a project's Claude Code data
                                        and settings, after asking
  stop|kill <id>                        Stop a background session. Its
                                        worktree is kept
  update|upgrade                        Check for updates and install if
                                        available

Examples:
  claude "explain this repository"
"""


def item(**fields):
    base = {"name": "claude", "kind": "binary", "version": "2.1.327", "arch": "linux-arm64",
            "url": "https://example.invalid/2.1.327/linux-arm64/claude", "entry": "claude",
            "sha256": "0" * 64}
    base.update(fields)
    return base


def script(directory, name, body):
    """An executable Python fake at directory/name."""
    path = Path(directory) / name
    path.write_text("#!" + sys.executable + "\n" + textwrap.dedent(body))
    path.chmod(0o755)
    return path


class Workdir(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.dir = Path(temporary.name)
        self.home = self.dir / "home"
        self.home.mkdir()


class ChecksumFindings(unittest.TestCase):
    LOCK = {"artifacts": {
        "claude": {"version": "2.1.327", "architectures": {
            "aarch64": {"arch": "linux-arm64", "sha256": "a" * 64},
            "x86_64": {"arch": "linux-x64", "sha256": "b" * 64}}},
        "rtk": {"version": "1.0.0", "architectures": {
            "aarch64": {"arch": "arm", "sha256": "c" * 64}}}}}

    def findings(self, published):
        def resolve(name):
            return (lambda version, arch: published(name, version, arch)) if name == "claude" else None
        return tools_lane.checksum_findings(self.LOCK, resolve)

    def test_matching_checksums_on_every_architecture_pass(self):
        table = {"linux-arm64": "a" * 64, "linux-x64": "b" * 64}
        self.assertEqual(self.findings(lambda name, version, arch: table[arch]), [])

    def test_a_differing_checksum_names_tool_version_and_architecture(self):
        table = {"linux-arm64": "a" * 64, "linux-x64": "e" * 64}
        findings = self.findings(lambda name, version, arch: table[arch])
        self.assertEqual(len(findings), 1)
        self.assertIn("claude 2.1.327 for x86_64", findings[0])
        self.assertIn("differs", findings[0])

    def test_a_failed_lookup_is_a_finding_not_a_crash(self):
        def missing(name, version, arch):
            raise ValueError("no such release")
        findings = self.findings(missing)
        self.assertEqual(len(findings), 2)
        self.assertTrue(all("no publisher checksum" in finding for finding in findings))

    def test_every_lookup_receives_the_locks_version(self):
        seen = []
        self.findings(lambda name, version, arch: seen.append((version, arch)) or "a" * 64)
        self.assertEqual(sorted(seen), [("2.1.327", "linux-arm64"), ("2.1.327", "linux-x64")])


class Download(Workdir):
    def fetch(self, payload, sha256):
        response = io.BytesIO(payload)
        with mock.patch.object(tools_lane, "urlopen", return_value=response):
            return tools_lane.download(item(sha256=sha256), self.dir)

    def test_a_matching_download_is_kept(self):
        payload = b"client bytes"
        path = self.fetch(payload, hashlib.sha256(payload).hexdigest())
        self.assertEqual(path, self.dir / "claude")
        self.assertEqual(path.read_bytes(), payload)

    def test_a_mismatch_raises_and_leaves_no_file(self):
        with self.assertRaises(tools_lane.LaneError):
            self.fetch(b"tampered", "0" * 64)
        self.assertEqual(list(self.dir.iterdir()), [self.home])


class Unpack(Workdir):
    def archive(self, names):
        path = self.dir / "client.tar.gz"
        with tarfile.open(path, "w:gz") as tar:
            for name in names:
                data = ("payload of " + name).encode()
                info = tarfile.TarInfo(name)
                info.size = len(data)
                tar.addfile(info, io.BytesIO(data))
        return path

    def assert_executable(self, path):
        self.assertTrue(path.stat().st_mode & stat.S_IXUSR)

    def test_a_binary_is_copied_and_made_executable(self):
        source = self.dir / "download"
        source.write_bytes(b"binary")
        out = tools_lane.unpack(item(), source, self.dir / "tools")
        self.assertEqual(out.read_bytes(), b"binary")
        self.assert_executable(out)

    def test_an_archive_yields_its_one_entry(self):
        archive = self.archive(["codex-package/README", "codex-package/bin/codex"])
        out = tools_lane.unpack(item(name="codex", kind="archive", entry="bin/codex"),
                                archive, self.dir / "tools")
        self.assertEqual(out.read_bytes(), b"payload of codex-package/bin/codex")
        self.assert_executable(out)

    def test_an_archive_without_exactly_one_entry_is_refused(self):
        for names in (["bin/other"], ["a/bin/codex", "b/bin/codex"]):
            with self.subTest(names=names):
                with self.assertRaises(tools_lane.LaneError):
                    tools_lane.unpack(item(name="codex", kind="archive", entry="bin/codex"),
                                      self.archive(names), self.dir / "tools")

    def test_a_zip_yields_its_one_entry(self):
        path = self.dir / "client.zip"
        with zipfile.ZipFile(path, "w") as bundle:
            bundle.writestr("cs-mcp-dir/cs-mcp", b"zip payload")
        out = tools_lane.unpack(item(name="cs-mcp", kind="zip", entry="cs-mcp"), path, self.dir / "tools")
        self.assertEqual(out.read_bytes(), b"zip payload")

    def test_an_unknown_kind_is_refused(self):
        with self.assertRaises(tools_lane.LaneError):
            tools_lane.unpack(item(kind="debs"), self.dir / "x", self.dir / "tools")


class ClaudeSubcommands(Workdir):
    def test_help_lists_each_name_and_alternative_but_no_wrapped_text(self):
        self.assertEqual(tools_lane.help_subcommands(HELP),
                         {"agents", "auth", "plugin", "plugins", "purge", "stop", "kill",
                          "update", "upgrade"})

    def test_the_launcher_list_comes_from_its_pass_through_case(self):
        names = tools_lane.alias_subcommands(ROOT / "infra/image/pairtask-agent-aliases.sh")
        self.assertIn("auth", names)
        self.assertIn("mcp", names)

    def test_a_launcher_without_the_case_is_a_finding(self):
        path = self.dir / "aliases.sh"
        path.write_text("alias claude=/usr/local/bin/claude\n")
        with self.assertRaises(tools_lane.LaneError):
            tools_lane.alias_subcommands(path)

    def fake_claude(self, version="2.1.327 (Claude Code)"):
        return script(self.dir, "claude", """
            import sys
            if sys.argv[1:] == ["--version"]:
                print({version!r})
            elif sys.argv[1:] == ["--help"]:
                sys.stdout.write({help!r})
            else:
                sys.exit(2)
            """.format(version=version, help=HELP))

    def findings(self, passed, version="2.1.327 (Claude Code)", locked="2.1.327"):
        with mock.patch.object(tools_lane, "alias_subcommands", return_value=set(passed)):
            return tools_lane.claude_findings(item(version=locked), self.fake_claude(version), self.home)

    def test_matching_lists_pass(self):
        listed = tools_lane.help_subcommands(HELP)
        self.assertEqual(self.findings(listed), [])

    def test_differences_are_reported_in_both_directions(self):
        listed = tools_lane.help_subcommands(HELP)
        findings = self.findings((listed - {"purge"}) | {"project"})
        self.assertEqual(len(findings), 2)
        self.assertTrue(any("subcommand purge" in f and "start a session" in f for f in findings))
        self.assertTrue(any("no subcommand project" in f for f in findings))

    def test_another_version_is_a_finding(self):
        listed = tools_lane.help_subcommands(HELP)
        findings = self.findings(listed, version="2.1.326 (Claude Code)")
        self.assertEqual(len(findings), 1)
        self.assertIn("2.1.327", findings[0])

    def test_a_version_that_only_starts_with_the_lock_is_a_finding(self):
        # The lock's 2.1.32 must not match a client reporting 2.1.327.
        listed = tools_lane.help_subcommands(HELP)
        findings = self.findings(listed, version="2.1.327 (Claude Code)", locked="2.1.32")
        self.assertEqual(len(findings), 1)

    def test_the_client_sees_a_private_home_and_nothing_else(self):
        probe = script(self.dir, "probe", """
            import json, os
            print(json.dumps(dict(os.environ)))
            """)
        with mock.patch.dict(os.environ, {"GH_TOKEN": "secret", "ANTHROPIC_API_KEY": "secret"}):
            seen = json.loads(tools_lane.run_client(probe, [], self.home).stdout)
        self.assertEqual(seen.get("HOME"), str(self.home))
        self.assertNotIn("GH_TOKEN", seen)
        self.assertNotIn("ANTHROPIC_API_KEY", seen)


class CodexHandshake(Workdir):
    CONFIG = 'model = "fixture"\n'

    SERVER = (
        "import json, os, sys\n"
        "assert sys.argv[1:] == ['app-server', '--listen', 'stdio://'], sys.argv\n"
        "home = os.environ['CODEX_HOME']\n"
        "config = open(os.path.join(home, 'config.toml')).read()\n"
        "request = json.loads(sys.stdin.readline())\n"
        "assert request['method'] == 'initialize' and request['id'] == 1\n"
        "def reply(request, home, config):\n"
        "{body}\n"
        "for line in reply(request, home, config):\n"
        "    sys.stdout.write(json.dumps(line) + '\\n')\n"
        "    sys.stdout.flush()\n"
        "sys.stdin.readline()\n")

    def server(self, reply):
        """A fake App Server: checks its argv and config, then writes the
        lines reply(request, home, config) returns."""
        body = textwrap.indent(textwrap.dedent(reply).strip("\n"), "    ")
        return script(self.dir, "codex", self.SERVER.format(body=body))

    def handshake(self, reply, timeout=10):
        config = self.dir / "config.toml"
        config.write_text(self.CONFIG)
        return tools_lane.codex_handshake(self.server(reply), self.home, config=config, timeout=timeout)

    OK = """
        ok = config == {config!r}
        return [{{"jsonrpc": "2.0", "id": 1, "result": {{
            "codexHome": home if ok else "/elsewhere", "userAgent": "codex_cli_rs/0.170.0"}}}}]
        """.format(config=CONFIG)

    def test_initialize_with_the_images_config_passes(self):
        result = self.handshake(self.OK)
        self.assertEqual(result["codexHome"], str(self.home / "codex-home"))

    def test_an_error_answer_is_refused(self):
        with self.assertRaises(tools_lane.LaneError):
            self.handshake('return [{"jsonrpc": "2.0", "id": 1, "error": {"code": -32600}}]')

    def test_another_codex_home_is_refused(self):
        with self.assertRaises(tools_lane.LaneError):
            self.handshake('return [{"jsonrpc": "2.0", "id": 1, "result": {"codexHome": "/other"}}]')

    def test_no_answer_is_refused_within_the_timeout(self):
        with self.assertRaises(tools_lane.LaneError):
            self.handshake("import time; time.sleep(30); return []", timeout=1)

    def test_closing_without_an_answer_is_refused(self):
        with self.assertRaises(tools_lane.LaneError):
            self.handshake("sys.exit(0)")

    def test_a_partial_line_does_not_outlast_the_timeout(self):
        # Bytes without a newline must not turn the deadline into a blocking read.
        reply = """
            sys.stdout.write('{"jsonrpc": "2.0", "id": 1, "res')
            sys.stdout.flush()
            import time; time.sleep(15)
            return []
            """
        started = time.monotonic()
        with self.assertRaises(tools_lane.LaneError):
            self.handshake(reply, timeout=1)
        self.assertLess(time.monotonic() - started, 10)

    def test_a_notification_before_the_answer_is_skipped(self):
        reply = """
            first = {"jsonrpc": "2.0", "method": "configWarning", "params": {}}
            return [first, {"jsonrpc": "2.0", "id": 1, "result": {
                "codexHome": home, "userAgent": "codex_cli_rs/0.170.0"}}]
            """
        self.assertEqual(self.handshake(reply)["codexHome"], str(self.home / "codex-home"))


class FirstAnswerBounds(unittest.TestCase):
    """A server that never stops sending notifications must still be cut off:
    by the deadline, and by the bytes it sent before answering. A stream that
    is always readable and a scripted clock make this exact; a read budget
    turns a missing bound into a failure instead of a hang."""

    NOTICE = b'{"jsonrpc": "2.0", "method": "notice", "params": {}}\n'
    BUDGET = 500

    class Stream:
        def fileno(self):
            return -1

    def answer(self, chunk, step, timeout):
        reads = []
        now = [0.0]

        def clock():
            now[0] += step
            return now[0]

        def read(fd, size):
            reads.append(size)
            if len(reads) > self.BUDGET:
                raise AssertionError("first_answer kept reading past its bounds")
            return chunk
        process = mock.Mock(stdout=self.Stream())
        with mock.patch.object(tools_lane.select, "select", side_effect=lambda r, w, x, t: (r, [], [])), \
                mock.patch.object(tools_lane.os, "read", side_effect=read), \
                mock.patch.object(tools_lane.time, "monotonic", side_effect=clock):
            with self.assertRaises(tools_lane.LaneError) as caught:
                tools_lane.first_answer(process, timeout)
        return str(caught.exception), len(reads)

    def test_continuous_notifications_stop_at_the_deadline(self):
        # Each clock reading advances 0.05 s, so a 1 s deadline passes within
        # a few dozen readings however readable the stream stays.
        message, reads = self.answer(self.NOTICE, step=0.05, timeout=1)
        self.assertLess(reads, 100)
        self.assertIn("in time", message)

    def test_notifications_before_the_answer_are_bounded_in_total(self):
        # The clock stands still, so only the byte bound can stop this: many
        # complete small lines that together pass MAX_PREAMBLE.
        burst = self.NOTICE * (65536 // len(self.NOTICE))
        message, reads = self.answer(burst, step=0.0, timeout=1)
        self.assertLessEqual(reads, tools_lane.MAX_PREAMBLE // len(burst) + 2)
        self.assertIn(str(tools_lane.MAX_PREAMBLE), message)


class ClientFindings(Workdir):
    LOCK = {"architectures": {"aarch64": {}}, "artifacts": {"claude": {
        "version": "2.1.327", "kind": "binary", "url": "https://example.invalid/{version}/{arch}/claude",
        "entry": "claude", "architectures": {"aarch64": {"arch": "linux-arm64", "sha256": "0" * 64}}}}}

    def test_a_bad_download_is_one_finding_naming_tool_version_and_architecture(self):
        for name in ("downloads", "tools", "home"):
            (self.dir / name).mkdir(exist_ok=True)
        with mock.patch.object(tools_lane, "urlopen", return_value=io.BytesIO(b"tampered")):
            findings = tools_lane.client_findings(self.LOCK, "aarch64", "claude", self.dir)
        self.assertEqual(len(findings), 1)
        self.assertTrue(findings[0].startswith("claude 2.1.327 for aarch64: "), findings[0])

    def test_a_client_that_exits_nonzero_is_one_finding(self):
        failing = subprocess.CalledProcessError(3, ["/x/claude", "--version"])
        with mock.patch.object(tools_lane, "download", return_value=self.dir / "d"), \
                mock.patch.object(tools_lane, "unpack", return_value=self.dir / "claude"), \
                mock.patch.object(tools_lane, "claude_findings", side_effect=failing), \
                mock.patch.dict(tools_lane.CLIENT_CHECKS, {"claude": tools_lane.claude_findings}):
            findings = tools_lane.client_findings(self.LOCK, "aarch64", "claude", self.dir)
        self.assertEqual(findings, ["claude 2.1.327 for aarch64: --version exited 3"])

    CS_MCP = {"architectures": {"aarch64": {}}, "artifacts": {"cs-mcp": {
        "version": "1.5.6", "kind": "zip", "url": "https://example.invalid/{version}/cs-mcp-{arch}.zip",
        "entry": "cs-mcp", "architectures": {"aarch64": {"arch": "linux-aarch64", "sha256": "0" * 64}}}}}

    def cs_mcp_findings(self, binary):
        with mock.patch.object(tools_lane, "download", return_value=self.dir / "d"), \
                mock.patch.object(tools_lane, "unpack", return_value=binary):
            return tools_lane.client_findings(self.CS_MCP, "aarch64", "cs-mcp", self.dir)

    def test_cs_mcp_is_run_and_reports_the_locks_version(self):
        # cs-mcp 1.5.6 prints "1.5.6" for --version.
        self.assertEqual(self.cs_mcp_findings(script(self.dir, "cs-mcp", 'print("1.5.6")\n')), [])

    def test_a_cs_mcp_of_another_version_is_a_finding(self):
        findings = self.cs_mcp_findings(script(self.dir, "cs-mcp", 'print("1.5.5")\n'))
        self.assertEqual(len(findings), 1)
        self.assertIn("1.5.6", findings[0])

    def test_a_failing_or_unrunnable_cs_mcp_is_a_finding(self):
        failing = script(self.dir, "cs-mcp", "import sys; sys.exit(4)\n")
        garbage = self.dir / "garbage"
        garbage.write_bytes(b"\x00not an executable\x00")
        garbage.chmod(0o755)
        for name, binary in (("exits 4", failing), ("not executable", garbage)):
            with self.subTest(case=name):
                findings = self.cs_mcp_findings(binary)
                self.assertEqual(len(findings), 1)
                self.assertTrue(findings[0].startswith("cs-mcp 1.5.6 for aarch64: "), findings[0])

    def test_an_architecture_missing_from_the_lock_is_refused(self):
        lock = self.dir / "lock.json"
        lock.write_text(json.dumps(self.LOCK))
        with self.assertRaises(tools_lane.LaneError):
            tools_lane.run(lock, "riscv64", self.dir / "work", resolve=lambda name: None)


class PublisherToken(unittest.TestCase):
    def requests(self, url, token):
        seen = []

        def capture(request, timeout):
            seen.append(request)
            return io.BytesIO(b"{}")
        environment = {"GH_TOKEN": token} if token else {}
        with mock.patch.object(sandbox_versions, "urlopen", side_effect=capture), \
                mock.patch.dict(os.environ, environment, clear=False):
            if not token:
                os.environ.pop("GH_TOKEN", None)
            sandbox_versions.publisher_json(url)
        return seen[0]

    def test_the_token_goes_to_the_github_api_only(self):
        api = self.requests("https://api.github.com/repos/openai/codex/releases/tags/x", "t0ken")
        self.assertEqual(api.get_header("Authorization"), "Bearer t0ken")
        for url in ("https://downloads.claude.ai/claude-code-releases/2.1.327/manifest.json",
                    "https://go.dev/dl/?mode=json&include=all",
                    "https://api.github.com.example.invalid/repos/x",
                    "https://github.com/openai/codex/releases"):
            with self.subTest(url=url):
                self.assertIsNone(self.requests(url, "t0ken").get_header("Authorization"))

    def test_without_a_token_nothing_is_added(self):
        api = self.requests("https://api.github.com/repos/openai/codex/releases/tags/x", None)
        self.assertIsNone(api.get_header("Authorization"))


if __name__ == "__main__":
    unittest.main()
