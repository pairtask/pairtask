"""Independent acceptance coverage for input-addressed optional tool bundles."""

import copy
import hashlib
import importlib.util
import json
from pathlib import Path
import shutil
import sys
import tempfile
import unittest
from unittest.mock import patch
from urllib.error import HTTPError
from types import SimpleNamespace
import zipfile

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / "scripts"))
import sandbox_artifacts as artifacts
import sandbox_bundle_release as bundles


def encoded(value):
    return (json.dumps(value, indent=2, sort_keys=True) + "\n").encode()


class BundleFixture(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.directory = Path(temporary.name)
        self.lock = json.loads((ROOT / "infra/image/tools.lock.json").read_text())
        self.inputs = bundles.bundle_inputs(self.lock, "aarch64")
        self.tag = "bundles-" + bundles.inputs_digest(self.inputs)
        self.manifest = {"architecture": "arm64", "release": self.tag, "bundles": {}}
        self.payload = {}
        for kind in self.inputs["kinds"]:
            name = f"collab-ai-bundle-{kind}-linux-arm64.tar.gz"
            data = (kind + " verified payload").encode()
            self.payload[name] = data
            self.manifest["bundles"][kind] = {"file": name, "sha256": hashlib.sha256(data).hexdigest()}
            if kind in ("chromium-deps", "docker"):
                self.manifest["bundles"][kind]["packages"] = {"package": "1.2.3"}
        self.manifest_name = "collab-ai-bundles-linux-arm64.json"
        self.inputs_name = "collab-ai-bundle-inputs-linux-arm64.json"
        self.payload[self.manifest_name] = encoded(self.manifest)
        self.payload[self.inputs_name] = encoded(self.inputs)

    def release(self, draft=False):
        return {"id": 123, "tag_name": self.tag, "draft": draft, "assets": [
            {"id": i, "name": name, "browser_download_url": "https://example.invalid/" + name}
            for i, name in enumerate(sorted(self.payload), 1)]}

    def write_payload(self, directory, payload=None):
        directory.mkdir(exist_ok=True)
        for name, data in (self.payload if payload is None else payload).items():
            (directory / name).write_bytes(data)

    def download(self, url, target):
        Path(target).write_bytes(self.payload[url.rsplit("/", 1)[-1]])

    def obtain(self, name, target):
        Path(target).write_bytes(self.payload[name])


class BundleInputTests(BundleFixture):
    def test_digest_is_canonical_and_uses_full_sha256(self):
        reordered = dict(reversed(list(self.inputs.items())))
        expected = hashlib.sha256(json.dumps(self.inputs, sort_keys=True, separators=(",", ":")).encode()).hexdigest()
        self.assertEqual(bundles.inputs_digest(reordered), expected)
        self.assertEqual(bundles.release_name(expected), "bundles-" + expected)

    def test_unrelated_tools_and_other_architecture_do_not_rebuild(self):
        changed = copy.deepcopy(self.lock)
        for name in ("codex", "claude", "go"):
            changed["artifacts"][name]["version"] = "999.0.0"
        changed["architectures"]["x86_64"]["base_image"] = "f" * 64
        changed["artifacts"]["node"]["architectures"]["x86_64"]["sha256"] = "e" * 64
        self.assertEqual(bundles.bundle_inputs(changed, "aarch64"), self.inputs)

    def test_each_effective_node_field_and_base_pin_changes_identity(self):
        paths = [("architectures", "aarch64", "base_image"),
                 *(('artifacts', 'node', key) for key in ("version", "kind", "url", "entry")),
                 ("artifacts", "node", "architectures", "aarch64", "sha256"),
                 ("artifacts", "node", "architectures", "aarch64", "arch")]
        for path in paths:
            changed = copy.deepcopy(self.lock)
            target = changed
            for key in path[:-1]:
                target = target[key]
            target[path[-1]] = "changed"
            with self.subTest(path=path):
                self.assertNotEqual(bundles.inputs_digest(bundles.bundle_inputs(changed, "aarch64")), self.tag[8:])

    def test_every_declared_recipe_file_changes_identity(self):
        root = self.directory / "recipe"
        for relative in bundles.INPUT_FILES:
            target = root / relative
            target.parent.mkdir(parents=True, exist_ok=True)
            shutil.copyfile(ROOT / relative, target)
        for relative in bundles.INPUT_FILES:
            target = root / relative
            original = target.read_bytes()
            target.write_bytes(original + b"\nchanged\n")
            with self.subTest(relative=relative):
                self.assertNotEqual(bundles.bundle_inputs(self.lock, "aarch64", root=root), self.inputs)
            target.write_bytes(original)

    def test_architecture_and_actual_kinds_are_bound(self):
        self.assertNotEqual(bundles.bundle_inputs(self.lock, "x86_64"), self.inputs)
        self.assertNotEqual(bundles.bundle_inputs(self.lock, "aarch64", ["docker"]), self.inputs)
        self.assertEqual(bundles.bundle_inputs(self.lock, "aarch64", list(reversed(self.inputs["kinds"]))), self.inputs)
        for kinds in (["unknown"], ["docker", "docker"]):
            with self.subTest(kinds=kinds), self.assertRaises(ValueError):
                bundles.bundle_inputs(self.lock, "aarch64", kinds)


class BundleReuseTests(BundleFixture):
    def test_verified_reuse_materializes_complete_offline_payload(self):
        with patch.object(bundles, "get_object", return_value=self.release()), \
                patch.object(bundles, "fetch", side_effect=self.download):
            self.assertEqual(bundles.reuse("owner/repo", self.inputs, self.directory), self.manifest)
        self.assertEqual({p.name: p.read_bytes() for p in self.directory.iterdir()}, self.payload)

    def test_absent_release_and_draft_are_build_misses(self):
        for state in (None, self.release(draft=True)):
            with self.subTest(state=state), patch.object(bundles, "get_object", return_value=state), \
                    patch.object(bundles, "fetch") as fetch:
                self.assertIsNone(bundles.reuse("owner/repo", self.inputs, self.directory))
                fetch.assert_not_called()

    def test_lookup_failure_is_not_a_rebuild(self):
        with patch.object(bundles, "get_object", side_effect=bundles.Unavailable("rate limited")):
            with self.assertRaises(bundles.Unavailable):
                bundles.reuse("owner/repo", self.inputs, self.directory)

    def test_published_release_requires_exact_unique_assets_and_tag(self):
        good = self.release()
        bad = [dict(good, tag_name="bundles-" + "0" * 64),
               dict(good, assets=good["assets"][:-1]),
               dict(good, assets=good["assets"] + [good["assets"][0]]),
               dict(good, assets=good["assets"] + [{"name": "unexpected"}])]
        for data in bad:
            with self.subTest(data=data), patch.object(bundles, "get_object", return_value=data):
                with self.assertRaises(bundles.IntegrityError):
                    bundles.published_bundles("owner/repo", self.inputs)

    def test_corrupt_payload_never_leaves_final_files(self):
        for name in self.payload:
            with self.subTest(name=name):
                original = self.payload[name]
                self.payload[name] = b"corrupt"
                with patch.object(bundles, "get_object", return_value=self.release()), \
                        patch.object(bundles, "fetch", side_effect=self.download):
                    with self.assertRaises(ValueError):
                        bundles.reuse("owner/repo", self.inputs, self.directory)
                self.payload[name] = original
                self.assertEqual(list(self.directory.iterdir()), [])

    def test_different_inputs_cannot_be_reused_under_requested_tag(self):
        changed = copy.deepcopy(self.inputs)
        changed["base_image"] = "0" * 64
        self.payload[self.inputs_name] = encoded(changed)
        with patch.object(bundles, "get_object", return_value=self.release()), \
                patch.object(bundles, "fetch", side_effect=self.download):
            with self.assertRaises(bundles.IntegrityError):
                bundles.reuse("owner/repo", self.inputs, self.directory)
        self.assertEqual(list(self.directory.iterdir()), [])

    def test_output_collision_preserves_existing_file_and_removes_owned_files(self):
        collision = sorted(self.payload)[-1]
        (self.directory / collision).write_bytes(b"already here")
        with patch.object(bundles, "get_object", return_value=self.release()), \
                patch.object(bundles, "fetch", side_effect=self.download):
            with self.assertRaises(FileExistsError):
                bundles.reuse("owner/repo", self.inputs, self.directory)
        self.assertEqual({p.name: p.read_bytes() for p in self.directory.iterdir()}, {collision: b"already here"})


class BundleSourceTests(BundleFixture):
    def source_record(self):
        return json.loads((self.directory / "bundle-source.json").read_text())["base"]

    def test_versioned_remote_manifest_selects_same_repo_bundle_release(self):
        artifacts.fetch_bundles(self.directory, "aarch64", self.obtain,
                                artifacts.BundleSource("https://example.invalid/workspace/", "owner/fork"))
        self.assertEqual(self.source_record(), f"https://github.com/owner/fork/releases/download/{self.tag}/")
        self.assertFalse(any(self.directory.glob("*.tar.gz")))

    def test_old_manifest_retains_workspace_source(self):
        del self.manifest["release"]
        self.payload[self.manifest_name] = encoded(self.manifest)
        artifacts.fetch_bundles(self.directory, "aarch64", self.obtain,
                                artifacts.BundleSource("https://example.invalid/workspace/", "owner/fork"))
        self.assertEqual(self.source_record(), "https://example.invalid/workspace/")

    def test_complete_local_source_remains_offline_even_with_release_reference(self):
        artifacts.fetch_bundles(self.directory, "aarch64", self.obtain, artifacts.BundleSource(None, "owner/fork"))
        self.assertEqual(self.source_record(), self.directory.resolve().as_uri() + "/")
        for name, data in self.payload.items():
            if name.endswith(".tar.gz"):
                self.assertEqual((self.directory / name).read_bytes(), data)

    def test_all_local_bundles_absent_uses_reference_without_downloading(self):
        def obtain(name, target):
            if name.endswith(".tar.gz"):
                raise FileNotFoundError(name)
            self.obtain(name, target)
        artifacts.fetch_bundles(self.directory, "aarch64", obtain, artifacts.BundleSource(None, "owner/fork"))
        self.assertEqual(self.source_record(), f"https://github.com/owner/fork/releases/download/{self.tag}/")

    def test_full_ci_zip_is_usable_offline(self):
        archive = self.directory / "ci.zip"
        with zipfile.ZipFile(archive, "w") as zip_file:
            for name, data in self.payload.items():
                zip_file.writestr(name, data)
        artifacts.fetch_bundles(self.directory, "aarch64",
                                lambda name, target: artifacts.from_zip(archive, name, target))
        self.assertEqual(self.source_record(), self.directory.resolve().as_uri() + "/")
        for name, data in self.payload.items():
            if name.endswith(".tar.gz"):
                self.assertEqual((self.directory / name).read_bytes(), data)

    def test_partial_or_corrupt_local_source_never_falls_back(self):
        for failure in (FileNotFoundError("missing"), HTTPError("url", 503, "offline", {}, None), None):
            def obtain(name, target):
                if "bundle-docker-" in name:
                    if failure is not None:
                        raise failure
                    Path(target).write_bytes(b"corrupt")
                else:
                    self.obtain(name, target)
            with self.subTest(failure=failure):
                with self.assertRaises((ValueError, HTTPError)):
                    artifacts.fetch_bundles(self.directory, "aarch64", obtain)
                self.assertFalse((self.directory / "bundle-source.json").exists())
                self.assertFalse(any(self.directory.glob("*.tar.gz")))

    def test_release_reference_cannot_escape_expected_namespace(self):
        for name in ("latest", "workspace-v1.0.0", "bundles-abc", "bundles-" + "A" * 64,
                     "bundles-" + "a" * 64 + "/../other", None):
            manifest = dict(self.manifest, release=name)
            with self.subTest(name=name), self.assertRaises(ValueError):
                artifacts.validate_bundles(manifest, "aarch64")


class BundlePublishTests(BundleFixture):
    def pull_payload(self, repo, release, directory):
        self.write_payload(directory)
        return directory

    def test_identical_published_payload_is_idempotent_without_writes(self):
        self.write_payload(self.directory)
        with patch.object(bundles, "find_tagged", return_value=self.release()), \
                patch.object(bundles, "pull_all", side_effect=self.pull_payload), patch.object(bundles, "run") as run:
            self.assertEqual(bundles.publish("owner/repo", self.directory, "aarch64"), "verified")
        run.assert_not_called()

    def test_different_published_bytes_are_not_adopted_or_overwritten(self):
        self.write_payload(self.directory)
        self.payload[self.manifest_name] += b"\n"
        with patch.object(bundles, "find_tagged", return_value=self.release()), \
                patch.object(bundles, "pull_all", side_effect=self.pull_payload), patch.object(bundles, "run") as run:
            with self.assertRaises(bundles.IntegrityError):
                bundles.publish("owner/repo", self.directory, "aarch64")
        run.assert_not_called()

    def test_invalid_local_payload_cannot_delete_existing_draft(self):
        self.write_payload(self.directory)
        (self.directory / self.manifest["bundles"]["docker"]["file"]).write_bytes(b"corrupt")
        with patch.object(bundles, "find_tagged", return_value=self.release(draft=True)), \
                patch.object(bundles, "run") as run:
            with self.assertRaises(bundles.IntegrityError):
                bundles.publish("owner/repo", self.directory, "aarch64")
        run.assert_not_called()

    def test_uploaded_payload_must_match_tested_bytes_even_if_self_consistent(self):
        self.write_payload(self.directory)
        name = self.manifest["bundles"]["docker"]["file"]
        self.payload[name] = b"different but internally consistent upload"
        self.manifest["bundles"]["docker"]["sha256"] = hashlib.sha256(self.payload[name]).hexdigest()
        self.payload[self.manifest_name] = encoded(self.manifest)
        with patch.object(bundles, "find_tagged", side_effect=[None, self.release(draft=True)]), \
                patch.object(bundles, "pull_all", side_effect=self.pull_payload), \
                patch.object(bundles, "published_bundles", return_value=self.release()), \
                patch.object(bundles, "run") as run:
            with self.assertRaises(bundles.IntegrityError):
                bundles.publish("owner/repo", self.directory, "aarch64")
        self.assertFalse(any(call.args[0][:3] == ["gh", "release", "edit"] for call in run.call_args_list))


class BundleBuildIntegrationTests(BundleFixture):
    def cli(self):
        spec = importlib.util.spec_from_file_location("bundle_cli_acceptance", ROOT / "scripts/sandbox-bundles.py")
        module = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(module)
        return module

    def test_successful_reuse_never_creates_a_builder(self):
        cli = self.cli()
        args = SimpleNamespace(reuse=True, repo="owner/fork", kinds=list(self.manifest["bundles"]))
        with patch.object(cli, "reuse", return_value=self.manifest) as reuse, \
                patch.object(cli, "Builder") as builder:
            self.assertEqual(cli.obtain_bundles(args, "aarch64", self.lock, self.directory), self.manifest)
        builder.assert_not_called()
        self.assertEqual(reuse.call_args.args, ("owner/fork", self.inputs, self.directory))

    def test_failed_reuse_never_builds_a_replacement(self):
        cli = self.cli()
        args = SimpleNamespace(reuse=True, repo="owner/fork", kinds=list(self.manifest["bundles"]))
        with patch.object(cli, "reuse", side_effect=bundles.IntegrityError("wrong published digest")), \
                patch.object(cli, "Builder") as builder:
            with self.assertRaises(bundles.IntegrityError):
                cli.obtain_bundles(args, "aarch64", self.lock, self.directory)
        builder.assert_not_called()


if __name__ == "__main__":
    unittest.main()
