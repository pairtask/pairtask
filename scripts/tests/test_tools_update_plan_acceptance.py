"""Publisher selection, immutable override plans and Incus error boundaries."""

import copy
import json
from pathlib import Path
import subprocess
import sys
from types import SimpleNamespace
import unittest
from unittest.mock import Mock, patch

ROOT = Path(__file__).resolve().parents[2]
sys.path[:0] = [str(ROOT / "scripts"), str(ROOT / "infra/image")]
import sandbox_tools_plan as plan
import sandbox_tools_update as update
import sandbox_versions as versions
import test_tool_update_acceptance as fixtures
import test_tools_update_host_acceptance as host_fixtures


class UpdatePlanAcceptance(unittest.TestCase):
    def setUp(self):
        self.fixture = fixtures.UpdateAcceptance()
        self.fixture.setUp()
        self.addCleanup(self.fixture.doCleanups)

    def test_exact_selection_never_consults_latest_for_pinned_clients(self):
        latest = Mock(return_value="3.0.0")
        self.assertEqual(plan.wanted_versions({"claude": "2.1.328"}, ["claude", "codex"], latest),
                         {"claude": "2.1.328", "codex": "3.0.0"})
        latest.assert_called_once_with("codex")
        with self.assertRaises(ValueError):
            plan.wanted_versions({"go": "1.0.0"}, ["claude"], latest)
        latest.assert_called_once_with("codex")

    def test_override_resolves_each_architecture_and_preserves_protected_fields(self):
        base = copy.deepcopy(self.fixture.base_lock)
        base["artifacts"]["claude"]["architectures"]["x86_64"] = {"arch": "amd64", "sha256": "f" * 64}
        original = copy.deepcopy(base)
        checksum = Mock(side_effect=lambda _version, arch: {"arm64": "a" * 64, "amd64": "b" * 64}[arch])
        lock = plan.override_lock(base, {"claude": "2.0.0"}, lambda name: checksum)
        self.assertEqual(base, original)
        expected = copy.deepcopy(base)
        expected["artifacts"]["claude"]["version"] = "2.0.0"
        expected["artifacts"]["claude"]["architectures"]["aarch64"]["sha256"] = "a" * 64
        expected["artifacts"]["claude"]["architectures"]["x86_64"]["sha256"] = "b" * 64
        self.assertEqual(lock, expected)
        self.assertEqual(checksum.call_count, 2)

    def test_manifest_can_return_to_release_lock_without_mutating_current(self):
        current = self.fixture.candidate()
        original = copy.deepcopy(current)
        manifest = plan.next_manifest(current, self.fixture.lock.read_bytes(), self.fixture.base_lock)
        self.assertEqual(current, original)
        self.assertNotIn("override", manifest)
        self.assertEqual(manifest["lock_sha256"], plan.digest(self.fixture.lock.read_bytes()))
        self.assertEqual(manifest["tools"]["go"], original["tools"]["go"])
        self.assertEqual(manifest["tools"]["claude"]["version"], "1.0.0")

    def test_latest_uses_publisher_channels_and_rejects_unexpected_tags(self):
        with patch.object(versions, "publisher_text", return_value="2.1.328") as text:
            self.assertEqual(versions.claude_latest(), "2.1.328")
            self.assertTrue(text.call_args.args[0].endswith("/latest"))
        for lookup, tag, expected in ((versions.codex_latest, "rust-v0.170.0", "0.170.0"),
                                      (versions.cs_mcp_latest, "MCP-1.5.22", "1.5.22")):
            with self.subTest(tag=tag), patch.object(versions, "publisher_json", return_value={"tag_name": tag}):
                self.assertEqual(lookup(), expected)
            with patch.object(versions, "publisher_json", return_value={"tag_name": "moving-latest"}):
                with self.assertRaises(ValueError):
                    lookup()


class UpdateAdapterAcceptance(unittest.TestCase):
    def instance(self):
        container = SimpleNamespace(name="workspace", command=["incus"], target="local:workspace",
                                    layout=SimpleNamespace(tools_activate="activate", share_dir="share"),
                                    output=Mock(), run=Mock())
        return plan.Instance(container)

    def test_nonobject_activator_reply_is_a_refusal_not_an_attribute_error(self):
        for value in (None, [], 42):
            with self.subTest(value=value):
                instance = self.instance()
                instance.container.output.return_value = json.dumps(value)
                with self.assertRaises(update.ContainerError) as error:
                    instance.activate("verify", "--update", "test")
                self.assertEqual(error.exception.code, "refused")

    def test_transport_error_classification_handles_text_and_bytes(self):
        for stderr in ("pairtask-tools-activate: foreign state", b"pairtask-tools-activate: foreign state"):
            with self.subTest(stderr=stderr):
                with self.assertRaises(update.ContainerError) as error:
                    with plan.transport("workspace"):
                        raise subprocess.CalledProcessError(1, ["incus"], stderr=stderr)
                self.assertEqual(error.exception.code, "refused")
        with self.assertRaises(update.ContainerError) as error:
            with plan.transport("workspace"):
                raise OSError("connection lost")
        self.assertEqual(error.exception.code, update.UNREACHABLE)

    def test_delivery_cleanup_never_masks_the_activator_failure(self):
        instance = self.instance()
        instance.container.output.side_effect = subprocess.CalledProcessError(
            1, ["incus"], stderr="pairtask-tools-activate: checksum refused")
        instance.container.run.side_effect = OSError("cleanup connection lost")
        with self.assertRaises(update.ContainerError) as error:
            instance.activate("stage", "manifest", "--incoming", "/tmp/delivery", "--update", "test")
        self.assertEqual(error.exception.code, "refused")
        self.assertIn("checksum", str(error.exception))

    def test_recovery_detects_changed_release_or_base_lock_on_same_instance(self):
        for changed in ("release_sha256", "base_lock_sha256"):
            with self.subTest(changed=changed):
                rig = host_fixtures.HostAcceptance()
                rig.setUp()
                self.addCleanup(rig.doCleanups)
                fingerprints = {"release_sha256": "release-a", "base_lock_sha256": "lock-a"}
                for box in rig.boxes.values():
                    box.provenance = lambda: dict(fingerprints)
                rig.stop_at("flipping")
                fingerprints[changed] += "-changed"
                rig.events.clear()
                result = rig.update().recover()
                self.assertEqual(result["failure"], "deployment-changed")
                self.assertEqual(rig.events, [])


if __name__ == "__main__":
    unittest.main()
