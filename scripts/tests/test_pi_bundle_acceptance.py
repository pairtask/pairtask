"""Pi bundle acceptance: offline fixtures exercise the code used by image builds."""

import importlib.util
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / "infra/image"))
sys.path.insert(0, str(ROOT / "scripts"))


def builder_module():
    spec = importlib.util.spec_from_file_location("pi_bundle_acceptance_builder", ROOT / "infra/image/build-pi-bundle.py")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


class PiBundleAcceptance(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory(prefix="pi-bundle-")
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        self.bundle = self.root / "bundle with spaces"
        self.bundle.mkdir()
        self.builder = builder_module()
        self.extension = self.root / "index.ts"
        self.extension.write_text('export default function extension(pi) { pi.testMarker = "fixture"; }\n')
        self.agent = self.add_package("@earendil-works/pi-coding-agent", "MIT", version="1.4.0", bin={"pi": "dist/cli.js"})
        (self.agent / "dist").mkdir()
        (self.agent / "dist/cli.js").write_text("console.log('1.4.0');\n")
        self.add_package("@earendil-works/pi-mcp", "MIT", version="1.4.0")
        self.nested = self.add_package("nested-dependency", "(MIT OR Apache-2.0)", parent=self.agent)
        (self.nested / "NOTICE").write_text("NESTED ATTRIBUTION MUST SURVIVE\n")

    def add_package(self, name, license, **fields):
        path = fields.pop("parent", self.bundle) / "node_modules" / name
        version = fields.pop("version", "2.3.4")
        path.mkdir(parents=True)
        (path / "package.json").write_text(json.dumps(dict(name=name, version=version, license=license, **fields)))
        (path / "LICENSE").write_text(f"LICENSE TEXT FOR {name}\n")
        return path

    def test_package_prunes_examples_but_keeps_runtime_and_all_notices(self):
        for parent in (self.agent, self.nested):
            example = parent / "examples" / "nested"
            example.mkdir(parents=True)
            (example / "doom.wasm").write_bytes(b"unshipped-example")
            (example / "package.json").write_text('{"name":"discarded-example","license":"GPL-3.0-only"}')
        (self.nested / "runtime.wasm").write_bytes(b"retained-runtime")
        details = self.builder.package(self.bundle, self.extension)
        self.assertEqual(details["pi_coding_agent"], "1.4.0")
        self.assertEqual(details["pi_mcp"], "1.4.0")
        self.assertEqual(details["node_min"], "22.19.0")
        self.assertEqual((self.bundle / details["cli"]).resolve(), (self.agent / "dist/cli.js").resolve())
        self.assertEqual(json.loads((self.bundle / "bundle.json").read_text()), details)
        self.assertEqual((self.bundle / "extension/index.ts").read_bytes(), self.extension.read_bytes())
        self.assertFalse(list(self.bundle.rglob("examples")))
        self.assertFalse(list(self.bundle.rglob("doom.wasm")))
        self.assertTrue((self.nested / "runtime.wasm").is_file())
        packages = self.builder.inventory(self.bundle)
        self.assertEqual({p["name"] for p in packages}, {
            "@earendil-works/pi-coding-agent", "@earendil-works/pi-mcp", "nested-dependency"})
        notices = (self.bundle / "THIRD_PARTY_NOTICES.txt").read_text()
        for package in packages:
            self.assertIn(f'{package["name"]}@{package["version"]}', notices)
            self.assertIn(package["license"], notices)
            self.assertIn(f'LICENSE TEXT FOR {package["name"]}', notices)
        self.assertIn("NESTED ATTRIBUTION MUST SURVIVE", notices)

    def test_doom_outside_examples_is_rejected(self):
        (self.nested / "doom.wasm").write_bytes(b"not-allowed")
        with self.assertRaisesRegex(ValueError, "doom"):
            self.builder.package(self.bundle, self.extension)

    def test_license_check_rejects_missing_unknown_copyleft_and_mixed_expressions(self):
        for license in (None, "", "GPL-3.0-only", "MIT OR GPL-3.0-only", "MIT AND Apache-2.0", "MIT OR", "Unlisted-License",
                        "(MIT", "MIT)", "((MIT)))", "MIT OR (Apache-2.0"):
            with self.subTest(license=license):
                package = {"name": "bad-dependency", "version": "1.0", "license": license, "license_files": []}
                with self.assertRaisesRegex(self.builder.LicenseError, "bad-dependency"):
                    self.builder.check_licenses([package])

    def test_allowed_license_expressions_and_every_offender_reported(self):
        allowed = ("MIT", "ISC", "Apache-2.0", "BSD-2-Clause", "BSD-3-Clause", "0BSD", "BlueOak-1.0.0",
                   "Unlicense", "CC0-1.0", "Python-2.0", "(MIT OR Apache-2.0)")
        self.builder.check_licenses([dict(name=f"allowed-{i}", version="1", license=value, license_files=[])
                                     for i, value in enumerate(allowed)])
        packages = [dict(name=name, version="1", license="GPL-3.0-only", license_files=[])
                    for name in ("first-bad", "second-bad")]
        with self.assertRaises(self.builder.LicenseError) as caught:
            self.builder.check_licenses(packages)
        for package in packages:
            self.assertIn(package["name"], str(caught.exception))


class PiBundleInputsAcceptance(unittest.TestCase):
    def test_disposable_activation_keeps_previous_release_tools_without_requesting_pi(self):
        import sandbox_rollout as rollout
        import sandbox_tools_install as install
        older = SimpleNamespace(bundles={"manifest": {"bundles": {
            "playwright": {}, "chromium-deps": {}, "docker": {}}}})
        self.assertEqual(set(install.shipped_tools(older)), {"cs-mcp", "playwright", "docker"})
        with tempfile.TemporaryDirectory() as temporary:
            image = Path(temporary)
            (image / "manifest.json").write_text('{"architecture":"aarch64"}')
            with patch.object(rollout, "tool_release", return_value=older), patch.object(rollout, "install_tools") as activate:
                rollout.activate_running("test-remote", "test-project", image)
            self.assertEqual(activate.call_count, 1)
            args, server, directory = activate.call_args.args
            self.assertEqual(set(args.tools), {"cs-mcp", "playwright", "docker"})
            self.assertEqual(args.remote, "test-remote")
            self.assertEqual(args.project, "test-project")
            self.assertEqual(server["architectures"], ["aarch64"])
            self.assertEqual(directory, image)

    def test_partial_bundle_sets_do_not_advertise_uninstallable_tools(self):
        import sandbox_tools_install as install
        partial = SimpleNamespace(bundles={"manifest": {"bundles": {"playwright": {}, "pi": {}}}})
        self.assertEqual(set(install.shipped_tools(partial)), {"cs-mcp", "pi"})
        self.assertEqual(set(install.shipped_tools(None)), {"cs-mcp"})
        self.assertEqual(set(install.shipped_tools(SimpleNamespace(bundles=None))), {"cs-mcp"})

    def test_explicit_pi_request_on_an_older_release_has_a_clear_error(self):
        import sandbox_tools_install as install
        older = SimpleNamespace(bundles={"manifest": {"bundles": {}}})
        with self.assertRaisesRegex(ValueError, "pi"):
            install.bundle_entries(older, install.selection(["pi"]), "workspace", True)

    def test_pi_selection_supplies_node_and_a_verified_workspace_bundle(self):
        import sandbox_artifacts as artifacts
        import sandbox_tools_install as install
        selected = install.selection(["pi"])
        self.assertTrue(selected["pi"])
        self.assertFalse(selected["playwright"])
        self.assertIn("node", install.tools_for("workspace", selected, store_mode=True))
        self.assertNotIn("pi", install.tools_for("secured", selected, store_mode=True))
        for machine, arch in (("aarch64", "arm64"), ("x86_64", "amd64")):
            details = {"file": f"collab-ai-bundle-pi-linux-{arch}.tar.gz", "sha256": "a" * 64,
                       "pi_coding_agent": "1.4.0", "pi_mcp": "1.4.0", "node_min": "22.19.0",
                       "cli": "node_modules/@earendil-works/pi-coding-agent/dist/cli.js"}
            manifest = {"architecture": arch, "bundles": {"pi": details}}
            artifacts.validate_bundles(manifest, machine)
            release = SimpleNamespace(bundles={"manifest": manifest})
            entries = install.bundle_entries(release, selected, "workspace", True)
            self.assertEqual(entries["pi"]["kind"], "archive")
            self.assertEqual(entries["pi"]["sha256"], details["sha256"])
            self.assertEqual(entries["pi"]["file"], details["file"])
            self.assertEqual(install.bundle_entries(release, selected, "secured", True), {})

    def test_pins_and_reuse_digest_cover_both_architectures_and_extension(self):
        import sandbox_bundle_release as release
        import sandbox_bundles as bundles
        self.assertIn("pi", bundles.BUNDLE_KINDS)
        package = json.loads((ROOT / "infra/image/pi/package.json").read_text())
        lock = json.loads((ROOT / "infra/image/pi/package-lock.json").read_text())
        for name in ("@earendil-works/pi-coding-agent", "@earendil-works/pi-mcp"):
            self.assertEqual(package["dependencies"][name], "1.4.0")
            self.assertEqual(lock["packages"]["node_modules/" + name]["version"], "1.4.0")
        required = {"infra/image/pi/package.json", "infra/image/pi/package-lock.json",
                    "infra/image/build-pi-bundle.py", "integrations/pi/index.ts"}
        self.assertTrue(required <= set(release.INPUT_FILES), "Pi inputs must invalidate bundle reuse")
        tools = json.loads((ROOT / "infra/image/tools.lock.json").read_text())
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            for name in release.INPUT_FILES:
                target = root / name
                target.parent.mkdir(parents=True, exist_ok=True)
                shutil.copyfile(ROOT / name, target)
            for machine in ("aarch64", "x86_64"):
                before = release.bundle_inputs(tools, machine, root=root)
                self.assertIn("pi", before["kinds"])
                extension = root / "integrations/pi/index.ts"
                extension.write_text(extension.read_text() + "\n// modified extension\n")
                after = release.bundle_inputs(tools, machine, root=root)
                self.assertNotEqual(release.inputs_digest(before), release.inputs_digest(after))


class PiRuntimePackagingAcceptance(unittest.TestCase):
    def test_shim_imports_default_from_the_wrapper_selected_bundle(self):
        with tempfile.TemporaryDirectory(prefix="pi shim ") as temporary:
            root = Path(temporary)
            extension = root / "extension"
            extension.mkdir()
            (extension / "index.ts").write_text('export default "selected-bundle-default";\n')
            shim = root / "shim.ts"
            shim.write_bytes((ROOT / "infra/image/pi-extension.ts").read_bytes())
            program = "const m = await import(process.argv[1]); console.log(m.default);"
            env = dict(os.environ, PAIRTASK_PI_BUNDLE=str(root))
            result = subprocess.run(["node", "--input-type=module", "-e", program, str(shim)],
                                    env=env, capture_output=True, text=True, timeout=10)
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual(result.stdout.strip(), "selected-bundle-default")
            env.pop("PAIRTASK_PI_BUNDLE")
            missing = subprocess.run(["node", "--input-type=module", "-e", program, str(shim)],
                                     env=env, capture_output=True, text=True, timeout=10)
            self.assertNotEqual(missing.returncode, 0)
            self.assertIn("PAIRTASK_PI_BUNDLE", missing.stderr)

    def test_wrapper_reports_missing_bundle(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            (root / "tool_store.py").write_text(
                "from pathlib import Path\n"
                "class StoreError(ValueError): pass\n"
                "def mode(): return 'store'\n"
                "def ready(): return None\n"
                f"class Store:\n current = Path({str(root / 'missing')!r})\n")
            program = "import runpy, sys, tool_store; runpy.run_path(sys.argv[1], run_name='__main__')"
            result = subprocess.run([sys.executable, "-c", program, str(ROOT / "infra/image/pairtask-pi")],
                                    env=dict(os.environ, PYTHONPATH=str(root)),
                                    capture_output=True, text=True, timeout=10)
            self.assertNotEqual(result.returncode, 0)
            self.assertEqual(result.stdout, "")
            self.assertIn("Pi is not installed in this sandbox", result.stderr)


if __name__ == "__main__":
    unittest.main()
