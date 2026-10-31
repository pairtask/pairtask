import hashlib
import io
import json
import os
from pathlib import Path
import sys
import tarfile
import tempfile
import unittest
import unittest.mock
import zipfile

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / "infra/image"))
import tool_store  # noqa: E402


def tar_bytes(entries):
    """entries: (name, kind, payload-or-target). kind: file|dir|sym|hard|dev."""
    buffer = io.BytesIO()
    with tarfile.open(fileobj=buffer, mode="w:gz") as tar:
        for name, kind, value in entries:
            info = tarfile.TarInfo(name)
            if kind == "file":
                data = value.encode()
                info.size, info.mode = len(data), 0o755
                tar.addfile(info, io.BytesIO(data))
                continue
            info.type = {"dir": tarfile.DIRTYPE, "sym": tarfile.SYMTYPE, "hard": tarfile.LNKTYPE,
                         "dev": tarfile.CHRTYPE}[kind]
            if kind in ("sym", "hard"):
                info.linkname = value
            tar.addfile(info)
    return buffer.getvalue()


def digest(data):
    return hashlib.sha256(data).hexdigest()


class StoreHarness(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        root = Path(self.temporary.name)
        self.incoming = root / "incoming"
        self.incoming.mkdir()
        self.store = tool_store.Store(root / "store", root / "state.json", root / "root-deps.json")
        # Activation fixtures must never discover or rewrite the live image's
        # configuration when these tests run inside a provisioned workspace.
        self.store.share, self.store.etc = root / "share", root / "etc"
        self.mode = root / "tools-mode"
        self.mode.write_text("store\n")
        self.lock, self.source = root / "lock.json", root / "source.json"
        self.lock.write_text("{}"), self.source.write_text("{}")

    def deliver(self, name, data, **spec):
        (self.incoming / f"{name}.bin").write_bytes(data)
        tool = {"kind": "archive", "entry": "bin/tool", "version": "1.0.0", **spec,
                "sha256": digest(data), "file": f"{name}.bin"}
        return {name: tool}

    def manifest(self, tools, selection=None):
        return {"release": digest(b"{}"), "lock_sha256": digest(b"{}"), "architecture": "aarch64",
                "selection": selection or {}, "tools": tools}

    def ready(self):
        return tool_store.ready(self.store, self.lock, self.source, self.mode)


class ActivationTests(StoreHarness):
    def test_activation_builds_a_generation_and_releases_the_root(self):
        tools = self.deliver("go", tar_bytes([("bin", "dir", None), ("bin/tool", "file", "#!/bin/sh\n")]))
        self.assertEqual(self.ready(), "sandbox tools are in maintenance; run the rollout to activate them")
        generation = self.store.activate(self.manifest(tools), self.incoming)
        self.assertEqual(self.store.resolve("go").read_text(), "#!/bin/sh\n")
        self.assertEqual(tool_store.read_state(self.store.state_path)["state"], "ready")
        self.assertEqual(self.store.status()["current"], generation)
        self.assertIsNone(self.ready())
        self.assertEqual(oct(self.store.artifacts.stat().st_mode & 0o777), "0o755")

    def test_ready_is_terminal_and_a_released_root_refuses_another_activation(self):
        tools = self.deliver("go", tar_bytes([("bin/tool", "file", "a")]))
        self.store.activate(self.manifest(tools), self.incoming)
        with self.assertRaisesRegex(tool_store.StoreError, "already has an activated generation"):
            self.store.activate(self.manifest(tools), self.incoming)

    def test_digest_mismatch_leaves_maintenance_state_and_nothing_staged(self):
        tools = self.deliver("go", tar_bytes([("bin/tool", "file", "a")]))
        tools["go"]["sha256"] = "0" * 64
        with self.assertRaisesRegex(tool_store.StoreError, "does not match its digest"):
            self.store.activate(self.manifest(tools), self.incoming)
        self.assertEqual(tool_store.read_state(self.store.state_path)["state"], "maintenance")
        self.assertEqual(self.store.complete_artifacts(), set())
        self.assertFalse(self.store.current.exists())
        self.assertTrue(self.ready())

    def test_release_mismatch_is_not_ready_in_store_mode_and_ignored_in_baked_mode(self):
        tools = self.deliver("go", tar_bytes([("bin/tool", "file", "a")]))
        self.store.activate(self.manifest(tools), self.incoming)
        self.source.write_text('{"other": true}')
        self.assertIn("different release", self.ready())
        self.mode.write_text("baked\n")
        self.assertIsNone(self.ready())

    def test_binary_and_zip_kinds_install_their_entry_executable(self):
        tools = self.deliver("claude", b"ELF", kind="binary", entry="claude")
        archive = io.BytesIO()
        with zipfile.ZipFile(archive, "w") as package:
            package.writestr("cs-mcp-linux-aarch64", "mcp")
            package.writestr("../escape", "no")
        tools.update(self.deliver("cs-mcp", archive.getvalue(), kind="zip", entry="cs-mcp-linux-aarch64"))
        self.store.activate(self.manifest(tools), self.incoming)
        for name in ("claude", "cs-mcp"):
            path = self.store.resolve(name)
            self.assertTrue(path.is_file() and os.access(path, os.X_OK), name)
        self.assertFalse(list(self.store.artifacts.rglob("escape")))


class ArchivePolicyTests(StoreHarness):
    def rejects(self, entries, pattern):
        tools = self.deliver("go", tar_bytes(entries))
        with self.assertRaisesRegex(tool_store.StoreError, pattern):
            self.store.activate(self.manifest(tools), self.incoming)
        self.assertEqual(self.store.complete_artifacts(), set())

    def test_escaping_and_absolute_names_are_rejected_before_extraction(self):
        self.rejects([("../bin/tool", "file", "a")], "escapes")
        self.rejects([("/bin/tool", "file", "a")], "escapes")
        self.rejects([("bin/../../tool", "file", "a")], "escapes")

    def test_duplicates_and_case_collisions_are_rejected(self):
        self.rejects([("bin/tool", "file", "a"), ("./bin/tool", "file", "b")], "Duplicate")
        self.rejects([("bin/tool", "file", "a"), ("bin/TOOL", "file", "b")], "colliding")

    def test_only_safe_member_types_and_inside_links_are_accepted(self):
        self.rejects([("bin/tool", "file", "a"), ("dev/null", "dev", None)], "Unsupported")
        self.rejects([("bin/tool", "file", "a"), ("bin/link", "sym", "../../etc/passwd")], "escapes")
        self.rejects([("bin/tool", "file", "a"), ("bin/link", "sym", "/etc/passwd")], "Absolute")
        self.rejects([("bin/link", "hard", "bin/missing"), ("bin/tool", "file", "a")], "unknown member")
        tools = self.deliver("go", tar_bytes([("bin", "dir", None), ("bin/tool", "file", "a"),
                                              ("bin/alias", "sym", "tool"), ("bin/copy", "hard", "bin/tool")]))
        self.store.activate(self.manifest(tools), self.incoming)
        self.assertEqual((self.store.resolve("go").parent / "alias").read_text(), "a")


class RealTarTests(StoreHarness):
    def test_archive_made_with_tar_dash_c_dot_is_accepted(self):
        source = Path(self.temporary.name) / "src"
        (source / "bin").mkdir(parents=True)
        (source / "bin/tool").write_text("ok")
        buffer = io.BytesIO()
        with tarfile.open(fileobj=buffer, mode="w:gz") as tar:
            tar.add(source, arcname=".")  # the layout `tar -C source .` produces, root entry "."
        tools = self.deliver("go", buffer.getvalue())
        self.store.activate(self.manifest(tools), self.incoming)
        self.assertEqual(self.store.resolve("go").read_text(), "ok")


class GenerationTests(StoreHarness):
    def activate_fresh(self, data):
        self.store.state_path.unlink(missing_ok=True)  # a new root
        tools = self.deliver("go", tar_bytes([("bin/tool", "file", data)]))
        return self.store.activate(self.manifest(tools), self.incoming), tools["go"]["sha256"]

    def test_prune_keeps_current_and_previous_generations_only(self):
        first, first_sha = self.activate_fresh("one")
        second, second_sha = self.activate_fresh("two")
        third, third_sha = self.activate_fresh("three")
        remaining = {path.name for path in self.store.generations.iterdir()}
        self.assertEqual(remaining, {second, third})
        self.assertEqual(self.store.complete_artifacts(), {second_sha, third_sha})
        self.assertEqual(self.store.resolve("go").read_text(), "three")

    def test_prune_keeps_actual_previous_despite_equal_or_reversed_timestamps(self):
        for older_time in (1_700_000_000, 1_700_000_100):
            with self.subTest(older_time=older_time):
                first, first_sha = self.activate_fresh("old-" + str(older_time))
                second, second_sha = self.activate_fresh("previous-" + str(older_time))
                os.utime(self.store.generations / first, (older_time, older_time))
                os.utime(self.store.generations / second, (1_700_000_000, 1_700_000_000))
                original_iterdir = Path.iterdir

                def older_first(path):
                    entries = list(original_iterdir(path))
                    if path == self.store.generations:
                        entries.sort(key=lambda item: item.name != first)
                    return iter(entries)

                # Equal timestamps must work even when the filesystem lists
                # the obsolete generation first. No sleeping or clock luck.
                with unittest.mock.patch.object(Path, "iterdir", older_first):
                    third, third_sha = self.activate_fresh("current-" + str(older_time))
                self.assertEqual({p.name for p in self.store.generations.iterdir()}, {second, third})
                self.assertEqual(self.store.complete_artifacts(), {second_sha, third_sha})
                self.assertFalse((self.store.artifacts / first_sha).exists())
                self.assertEqual(self.store.resolve("go").read_text(), "current-" + str(older_time))

    def test_reactivation_keeps_the_previous_generation_and_its_artifacts(self):
        first, first_sha = self.activate_fresh("previous")
        second, second_sha = self.activate_fresh("current")
        tools = json.loads((self.store.generations / second / "manifest.json").read_text())
        self.store.state_path.unlink()
        self.assertEqual(self.store.activate(tools, self.incoming), second)
        self.assertEqual({p.name for p in self.store.generations.iterdir()}, {first, second})
        self.assertEqual(self.store.complete_artifacts(), {first_sha, second_sha})
        self.assertEqual(self.store.resolve("go").read_text(), "current")

    def test_incomplete_leftovers_are_removed_before_staging(self):
        (self.store.artifacts).mkdir(parents=True)
        (self.store.artifacts / "garbage").mkdir()
        (self.store.generations).mkdir()
        (self.store.generations / "half").mkdir()
        self.activate_fresh("one")
        self.assertFalse((self.store.artifacts / "garbage").exists())
        self.assertFalse((self.store.generations / "half").exists())



class DebsAndConfigurationTests(StoreHarness):
    def setUp(self):
        super().setUp()
        root = Path(self.temporary.name)
        self.store.share, self.store.etc = root / "share", root / "etc"
        self.store.share.mkdir()
        (self.store.share / "mcp-servers.json").write_text(json.dumps({
            "codescene": {"command": "/usr/local/bin/cs-mcp", "args": []},
            "playwright": {"command": "/usr/local/lib/pairtask/pairtask-playwright", "args": []}}))
        (self.store.share / "claude-mcp.base.json").write_text(json.dumps({"mcpServers": {"pairtask": {"command": "/usr/local/lib/pairtask/pairtask-mcp"}}}))
        (self.store.share / "codex-config.base.toml").write_text("[sandbox_workspace_write]\nwritable_roots = []\n")
        self.calls = []

        def fake_run(command, **kwargs):
            self.calls.append(command)
            if command[0] == "dpkg-query":
                return type("Result", (), {"returncode": 0, "stdout": "installed 1.2-3\n"})()
            return type("Result", (), {"returncode": 0, "stdout": ""})()
        self.store.run = fake_run

    def test_deb_sets_install_once_per_root_and_are_verified_every_time(self):
        debs = tar_bytes([("libnss3_1.2-3_arm64.deb", "file", "deb")])
        tools = self.deliver("chromium-deps", debs, kind="debs", entry=".", packages={"libnss3": "1.2-3"})
        with unittest.mock.patch.object(tool_store, "POLICY_RC", Path(self.temporary.name) / "policy-rc.d"):
            self.store.activate(self.manifest(tools), self.incoming)
        installs = [c for c in self.calls if c[:2] == ["dpkg", "-i"]]
        self.assertEqual(len(installs), 1)
        self.assertTrue(installs[0][-1].endswith("libnss3_1.2-3_arm64.deb"))
        self.assertIn(tools["chromium-deps"]["sha256"], tool_store.read_json(self.store.root_deps)["installed"])
        self.assertFalse((Path(self.temporary.name) / "policy-rc.d").exists())
        self.assertIsNone(self.store.resolve("chromium-deps"))
        # A new root with the artifact already cached: no install record → dpkg runs again; verification always.
        self.calls.clear()
        self.store.state_path.unlink()
        self.store.root_deps.unlink()
        with unittest.mock.patch.object(tool_store, "POLICY_RC", Path(self.temporary.name) / "policy-rc.d"):
            self.store.activate(self.manifest(tools), self.incoming)
        self.assertEqual(len([c for c in self.calls if c[:2] == ["dpkg", "-i"]]), 1)
        self.assertTrue(any(c[0] == "dpkg-query" for c in self.calls))

    def test_a_missing_package_fails_the_activation_before_the_flip(self):
        debs = tar_bytes([("libnss3_1.2-3_arm64.deb", "file", "deb")])
        tools = self.deliver("chromium-deps", debs, kind="debs", entry=".", packages={"libnss3": "9.9"})
        with unittest.mock.patch.object(tool_store, "POLICY_RC", Path(self.temporary.name) / "policy-rc.d"):
            with self.assertRaisesRegex(tool_store.StoreError, "not installed at 9.9"):
                self.store.activate(self.manifest(tools), self.incoming)
        self.assertFalse(self.store.current.exists())
        self.assertEqual(tool_store.read_state(self.store.state_path)["state"], "maintenance")

    def test_configuration_and_markers_follow_the_selection(self):
        tools = self.deliver("go", tar_bytes([("go/bin/go", "file", "a")]), entry="go/bin/go")
        selection = {"go": True, "cs-mcp": True, "playwright": False, "docker": True}
        self.store.activate(self.manifest(tools, selection), self.incoming)
        claude = json.loads((self.store.etc / "pairtask/claude-mcp.json").read_text())
        self.assertEqual(set(claude["mcpServers"]), {"pairtask", "codescene"})
        codex = (self.store.etc / "codex/config.toml").read_text()
        self.assertIn("[mcp_servers.codescene]", codex)
        self.assertNotIn("playwright", codex)
        markers = self.store.etc / "pairtask/tools"
        self.assertTrue((markers / "cs-mcp.enabled").exists() and (markers / "docker.enabled").exists())
        self.assertFalse((markers / "playwright.enabled").exists())

    def test_launcher_names_resolve_siblings_and_go_tools(self):
        tools = self.deliver("go", tar_bytes([("go/bin/go", "file", "a"), ("go/bin/gofmt", "file", "b")]), entry="go/bin/go")
        node = tar_bytes([("node-v1/bin/node", "file", "n"), ("node-v1/bin/npm", "file", "m")])
        tools.update(self.deliver("node", node, entry="node-v1/bin/node"))
        self.store.activate(self.manifest(tools), self.incoming)
        self.assertEqual(self.store.resolve_name("gofmt").read_text(), "b")
        self.assertEqual(self.store.resolve_name("npm").read_text(), "m")
        self.assertEqual(self.store.resolve_name("go"), self.store.resolve("go"))
        self.assertIsNone(self.store.resolve_name("claude"))


if __name__ == "__main__":
    unittest.main()
