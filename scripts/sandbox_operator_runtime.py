"""Disposable Incus operator with pinned OpenTofu and private admin/state proxies."""

import hashlib
import json
from pathlib import Path
import subprocess
import tempfile
import time
import uuid
import zipfile

from sandbox_base_image import DEFAULT_REPO, Store, ensure_base
from sandbox_operator_config import ROOT
from sandbox_operator_download import download_archive
import sandbox_operator_prepared as prepared

# Fixed by the operator recipe; the prepared image path must launch with exactly these.
INIT_OPTIONS = ("--no-profiles", "-c", "security.privileged=false",
                "-c", "security.idmap.isolated=true", "-c", "security.nesting=false",
                "-c", "security.guestapi=false", "-c", "boot.autostart=false",
                "-c", "limits.cpu=2", "-c", "limits.memory=2GiB", "-c", "limits.processes=256",
                "-d", "root,size=8GiB")


class Operator:
    def __init__(self, remote, server, pool, network):
        self.remote = remote + ":"
        self.server, self.pool, self.network = server, pool, network
        self.incus, self.prepared = subprocess.run, None
        self.created = False
        self.verified = False  # launched from a prepared image and validated
        self.place("pairtask-operator-" + uuid.uuid4().hex[:16], "operator")

    def using(self, incus=subprocess.run, prepared_store=None):
        """Inject the Incus runner and the run's prepared-image store; returns self."""
        self.incus, self.prepared = incus, prepared_store
        return self

    def place(self, project, name):
        self.project, self.name = project, name
        self.target = self.remote + name
        self.base = ["incus", "--project", project]

    def scratch_in(self, project):
        """A sibling operator that prepares the image inside the store's project."""
        scratch = Operator(self.remote[:-1], self.server, self.pool, self.network).using(self.incus)
        scratch.place(project, "scratch-" + uuid.uuid4().hex[:12])
        return scratch

    @property
    def arch(self):
        return self.server["architectures"][0]

    def run(self, *args, **kwargs):
        return self.incus(self.base + list(args), check=True, **kwargs)

    def execute(self, *args, **kwargs):
        return self.run("exec", self.target, "-T", "--", *args, **kwargs)

    def push(self, source, destination):
        self.run("file", "push", str(source), self.target + destination)

    def base_fingerprint(self):
        # The operator deliberately uses the host checkout's pin, not the release's.
        image_lock = json.loads((ROOT / "infra/image/tools.lock.json").read_text())
        return image_lock["architectures"][self.arch]["base_image"]

    def create_project(self):
        self.incus(["incus", "project", "create", self.remote + self.project,
                    "-c", "features.images=true", "-c", "features.profiles=true",
                    "-c", "features.networks=false"], check=True)
        self.created = True

    def launch(self, image):
        self.run("init", image, self.target, "--storage", self.pool, "--network", self.network, *INIT_OPTIONS)
        self.run("start", self.target)

    def create(self, repo=DEFAULT_REPO):
        self.create_project()
        if self.prepared is not None and self.launch_prepared(repo):
            return
        self.launch(ensure_base(Store(self.remote[:-1], self.project), self.base_fingerprint(), repo).ref)
        self.bootstrap()

    def bootstrap(self):
        self.execute("mkdir", "-p", "/operator/config", "/operator/bin")
        self.execute("env", "DEBIAN_FRONTEND=noninteractive", "apt-get", "-o", "Acquire::Retries=3", "update", "-qq")
        self.execute("env", "DEBIAN_FRONTEND=noninteractive", "apt-get", "install", "-y",
                     "--no-install-recommends", "python3", "ca-certificates")

    def launch_prepared(self, repo):
        """Launch from the run's prepared image. A failure to prepare, publish or
        copy falls back to the full bootstrap in this same fresh project; a
        launched container that fails validation is deleted and raises."""
        key = prepared.recipe_key(self.arch, self.base_fingerprint(), prepared.operator_lock_bytes(),
                                  prepared.recipe_bytes())
        try:
            fingerprint = self.prepared_image(key, repo)
            self.prepared.copy_into(fingerprint, self.project)
        except (subprocess.CalledProcessError, OSError, RuntimeError, prepared.Contaminated) as error:
            print(f"prepared: fallback ({type(error).__name__}: {error})", flush=True)
            self.discard_images()
            return False
        self.launch(fingerprint)
        try:
            snapshot = prepared.inspect(self.incus, self.remote[:-1], self.project, self.name)
            prepared.verify_launch(snapshot, prepared.expected_binary_sha256(self.arch))
        except prepared.Contaminated:
            self.run("delete", self.target, "--force")
            self.prepared.discard(key)
            raise
        self.verified = True
        return True

    def prepared_image(self, key, repo):
        fingerprint = self.prepared.find(key)
        if fingerprint is not None:
            print(f"prepared: reused {key[:12]}", flush=True)
            return fingerprint
        start = time.monotonic()
        self.prepared.ensure_project()
        scratch = self.scratch_in(self.prepared.project)
        try:
            scratch.launch(ensure_base(Store(self.remote[:-1], self.prepared.project), self.base_fingerprint(), repo).ref)
            scratch.bootstrap()
            with tempfile.TemporaryDirectory() as directory:
                scratch.install(Path(directory))
            scratch.execute(*prepared.SANITIZE)
            scratch.run("stop", scratch.target)
            # Checked after the stop, so a shutdown can't add identity to the image.
            snapshot = prepared.inspect(self.incus, self.remote[:-1], self.prepared.project, scratch.name)
            prepared.verify_clean(snapshot, prepared.expected_binary_sha256(self.arch))
            fingerprint = self.prepared.publish(scratch.name, key)
        except BaseException:
            self.prepared.discard(key)
            raise
        finally:
            scratch.incus(scratch.base + ["delete", scratch.target, "--force"])
        print(f"prepared: built {key[:12]} in {prepared.timed(start)}", flush=True)
        return fingerprint

    def discard_images(self):
        """Remove any partially copied image from this operation's project."""
        images = self.incus(self.base + ["image", "list", self.remote, "--format=json"],
                            capture_output=True, text=True)
        for image in json.loads(images.stdout or "[]") if images.returncode == 0 else []:
            self.incus(self.base + ["image", "delete", self.remote + image["fingerprint"]])

    def install(self, temporary):
        if self.verified:
            return  # the prepared executable was checked against its pin at launch
        lock = json.loads((ROOT / "infra/operator/tools.lock.json").read_text())
        item = lock["architectures"][self.server["architectures"][0]]
        url = f"https://github.com/opentofu/opentofu/releases/download/v{lock['tofu_version']}/{item['archive']}"
        archive = temporary / "tofu.zip"
        archive.write_bytes(download_archive(url))
        if hashlib.sha256(archive.read_bytes()).hexdigest() != item["sha256"]:
            raise ValueError("OpenTofu archive checksum mismatch")
        with zipfile.ZipFile(archive) as package:
            executable = package.read("tofu")
        # The archive pin covers packaging; this pin covers the executable we run.
        if hashlib.sha256(executable).hexdigest() != item["binary_sha256"]:
            raise ValueError("OpenTofu executable checksum mismatch")
        binary = temporary / "tofu"
        binary.write_bytes(executable)
        self.push(binary, "/operator/bin/tofu")
        self.execute("chmod", "755", "/operator/bin/tofu")
        self.execute("/operator/bin/tofu", "version")

    def connect(self, state_connection):
        # The operator is trusted administration. No workspace or control profile
        # receives either proxy, and no admin TCP listener is created.
        self.run("config", "device", "add", self.target, "admin", "proxy", "bind=instance",
                 "listen=unix:/run/operator-incus.sock", "connect=unix:/var/lib/incus/unix.socket",
                 "uid=0", "gid=0", "mode=0600")
        self.run("config", "device", "add", self.target, "state", "proxy", "bind=instance",
                 "listen=tcp:127.0.0.1:8080", "connect=" + state_connection)

    def cleanup(self):
        if not self.created:
            return
        instances = self.run("list", self.remote, "--format=json", capture_output=True, text=True)
        for instance in json.loads(instances.stdout):
            self.run("delete", self.remote + instance["name"], "--force")
        images = self.run("image", "list", self.remote, "--format=json", capture_output=True, text=True)
        for image in json.loads(images.stdout):
            self.run("image", "delete", self.remote + image["fingerprint"])
        self.incus(["incus", "project", "delete", self.remote + self.project], check=True)
