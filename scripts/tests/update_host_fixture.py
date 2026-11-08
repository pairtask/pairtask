"""External boundaries for the real public host CLI, never an updater model."""

import contextlib
import copy
import io
import json
import os
from pathlib import Path
import runpy
import subprocess
import sys
import tempfile
from unittest import mock
from urllib.error import HTTPError


SCRIPTS = Path(os.environ.get("PAIRTASK_TEST_SCRIPTS", Path(__file__).resolve().parents[1]))
TAG = "workspace-v0.0.0-build.{}.{}"
REPO = "pairtask/pairtask"
OLD = TAG.format(99, 1)
NEW = TAG.format(100, 1)


def provenance(commit):
    return {"collab": {"commit": commit, "requested_ref": commit, "working_tree": False},
            "recipe_commit": commit, "files_sha256": {"scripts/pairtask.py": "c" * 64}}


def manifest(commit, image):
    return dict(provenance(commit), architecture="aarch64", image_fingerprint=image, tools={})


class HostBoundary:
    """Fake only Incus, Git and HTTPS; run the production command dispatcher."""

    def __init__(self, test):
        self.test = test
        self.directory = tempfile.TemporaryDirectory()
        test.addCleanup(self.directory.cleanup)
        self.root = Path(self.directory.name)
        self.repo, self.project, self.remote = REPO, "pairtask", "local"
        self.host_tag, self.host_commit, self.dirty = NEW, "b" * 40, False
        self.latest, self.http_error = NEW, None
        self.manifests = {OLD: manifest("a" * 40, "1" * 64), NEW: manifest("b" * 40, "2" * 64)}
        self.instances = {name: self.instance(OLD) for name in ("secured", "workspace")}
        self.processes, self.requests = [], []
        self.notice_supported = True
        self.latest_fields = {}

    def asset_id(self, tag):
        return list(self.manifests).index(tag) + 1

    def instance(self, tag):
        data = self.manifests[tag]
        return {"status": "Running", "architecture": "aarch64",
                "config": {"volatile.base_image": data["image_fingerprint"]},
                "source": {key: copy.deepcopy(data[key]) for key in provenance("x")}}

    def deploy(self, tag):
        self.instances = {name: self.instance(tag) for name in self.instances}

    def release(self, tag):
        return {"tag_name": tag, "draft": False, "prerelease": False,
                "target_commitish": self.manifests[tag]["collab"]["commit"],
                "assets": [{"id": self.asset_id(tag), "name": "collab-ai-workspace-linux-arm64.json",
                            "browser_download_url": f"https://github.com/{self.repo}/releases/download/{tag}/collab-ai-workspace-linux-arm64.json"}]}

    def http(self, request, timeout=None, **_kwargs):
        url = getattr(request, "full_url", request)
        self.requests.append(url)
        self.test.assertIsNotNone(timeout, "all metadata requests need a deadline")
        self.test.assertLessEqual(timeout, 5)
        if self.http_error:
            raise HTTPError(url, self.http_error, "temporary", {}, None)
        return io.BytesIO(json.dumps(self.response(url)).encode())

    def response(self, url):
        api = f"https://api.github.com/repos/{self.repo}/releases"
        if url == api + "/latest":
            return dict(self.release(self.latest), **self.latest_fields)
        if url == api + "?per_page=50":
            return [self.release(tag) for tag in self.manifests]
        prefix = api + "/assets/"
        self.test.assertTrue(url.startswith(prefix), "unexpected lookup: " + url)
        asset_id = int(url[len(prefix):])
        tag = next(tag for tag in self.manifests if self.asset_id(tag) == asset_id)
        return self.manifests[tag]

    def process(self, argv, **kwargs):
        self.processes.append(list(argv))
        self.test.assertFalse(kwargs.get("shell", False), "metadata must remain literal argv")
        if argv[0] == "git":
            return self.git(argv)
        self.test.assertEqual(argv[:3], ["incus", "--project", self.project])
        self.test.assertGreater(kwargs.get("timeout", 0), 0, "Incus reads must be bounded")
        return self.incus(argv)

    def git(self, argv):
        self.test.assertEqual(argv[1], "-C")
        self.test.assertEqual(Path(argv[2]).resolve(), SCRIPTS.resolve().parent)
        command = argv[3:]
        answers = {("rev-parse", "HEAD"): self.host_commit,
                   ("tag", "--points-at", "HEAD"): self.host_tag,
                   ("status", "--porcelain", "--untracked-files=no"): " M scripts/pairtask.py" if self.dirty else ""}
        self.test.assertIn(tuple(command), answers)
        return subprocess.CompletedProcess(argv, 0, answers[tuple(command)] + "\n", "")

    def incus(self, argv):
        if argv[3:] == ["list", self.remote + ":", "--format=json"]:
            records = [dict(data, name=name) for name, data in self.instances.items()]
            return subprocess.CompletedProcess(argv, 0, json.dumps(records), "")
        self.test.assertEqual(argv[3], "exec", "version check may never mutate deployment")
        self.test.assertTrue(argv[4].startswith(self.remote + ":"))
        self.test.assertEqual(argv[5:7], ["-T", "--"])
        return self.exec(argv)

    def exec(self, argv):
        if argv[7:9] == ["cat", "/usr/local/share/pairtask/build-source.json"]:
            name = argv[4].split(":", 1)[1]
            self.test.assertEqual(self.instances[name]["status"], "Running", "observation must not start stopped containers")
            return subprocess.CompletedProcess(argv, 0, json.dumps(self.instances[name]["source"]), "")
        self.test.assertEqual(argv[7:11], ["pairtask", "operator", "update-notice", "set"])
        code = 0 if self.notice_supported else 2
        return subprocess.CompletedProcess(argv, code, "{}", "unknown command" if code else "")

    def check(self, refresh=True):
        flags = ["version", "--json", "--project", self.project, "--remote", self.remote]
        if refresh:
            flags.append("--refresh")
        return self.command(flags)

    def command(self, flags):
        output, errors = io.StringIO(), io.StringIO()
        env = {"XDG_STATE_HOME": str(self.root), "HOME": str(self.root),
               "PAIRTASK_HOME": str(self.root / "unrelated-checkout"), "GITHUB_REPOSITORY": self.repo}
        with mock.patch.dict(os.environ, env), mock.patch.object(sys, "path", [str(SCRIPTS), *sys.path]), \
                mock.patch("subprocess.run", side_effect=self.process), \
                mock.patch("urllib.request.urlopen", side_effect=self.http), \
                contextlib.redirect_stdout(output), contextlib.redirect_stderr(errors):
            cli = runpy.run_path(str(SCRIPTS / "pairtask.py"))
            try:
                code = cli["main"](flags)
            except SystemExit as error:
                code = error.code
        self.test.assertEqual(code, 0, output.getvalue() + errors.getvalue())
        result = json.loads(output.getvalue())
        self.test.assertEqual(result["schema"], 1)
        self.test.assertEqual((result["repo"], result["project"], result["remote"]),
                              (self.repo, self.project, self.remote))
        return result
