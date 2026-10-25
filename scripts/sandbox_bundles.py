"""Optional tool bundles, built inside a container of the pinned base image.

A bundle is one digest-pinned release asset the tool store installs offline:
the Playwright MCP closure, the apt packages Chromium needs, and Docker's apt
packages. Building them in the base image makes the package closure exactly
what the slim image lacks.
"""

import json
from pathlib import Path
import re

from sandbox_artifacts import sha256

ROOT = Path(__file__).resolve().parents[1]
DEBS = "/root/debs"
TAR_FLAGS = ["--sort=name", "--owner=0", "--group=0", "--numeric-owner", "--mtime=@0"]

# Playwright's Ubuntu 24.04 dependency table for Chromium, without xvfb: the
# MCP runs Chromium headless.
CHROMIUM_LIBS = [
    "libasound2t64", "libatk-bridge2.0-0t64", "libatk1.0-0t64", "libatspi2.0-0t64", "libcairo2",
    "libcups2t64", "libdbus-1-3", "libdrm2", "libgbm1", "libglib2.0-0t64", "libnspr4", "libnss3",
    "libpango-1.0-0", "libx11-6", "libxcb1", "libxcomposite1", "libxdamage1", "libxext6", "libxfixes3",
    "libxkbcommon0", "libxrandr2",
]
CHROMIUM_FONTS = [
    "fonts-noto-color-emoji", "fonts-unifont", "libfontconfig1", "libfreetype6", "xfonts-cyrillic",
    "xfonts-scalable", "fonts-liberation", "fonts-ipafont-gothic", "fonts-wqy-zenhei",
    "fonts-tlwg-loma-otf", "fonts-freefont-ttf",
]
DOCKER_SUPPORT = ["uidmap", "dbus-user-session", "slirp4netns", "fuse-overlayfs", "bubblewrap",
                  "apparmor", "apparmor-profiles"]
DOCKER_KEY = "https://download.docker.com/linux/ubuntu/gpg"
BUNDLE_KINDS = ("playwright", "chromium-deps", "docker", "pi")


def bundle_file(kind, arch):
    return f"collab-ai-bundle-{kind}-linux-{arch}.tar.gz"


def manifest_file(arch):
    return f"collab-ai-bundles-linux-{arch}.json"


def docker_packages(root=ROOT):
    return (root / "infra/image/docker-packages.txt").read_text().split()


def deb_inventory(listing):
    """{package: version} from the archive file names apt downloaded."""
    inventory = {}
    for name in listing.split():
        match = re.fullmatch(r"([a-z0-9][a-z0-9+.-]*)_([^_]+)_[a-z0-9]+\.deb", name)
        if match is None:
            raise ValueError(f"Unexpected file in the package download directory: {name}")
        inventory[match.group(1)] = match.group(2).replace("%3a", ":")
    return inventory


class BundleBuild:
    """One bundle build: a started base-image container, the release
    architecture label and the asset output directory."""

    def __init__(self, builder, arch, output, lock_file=ROOT / "infra/image/tools.lock.json"):
        self.builder, self.arch, self.output, self.lock_file = builder, arch, output, Path(lock_file)

    def archive(self, kind, source):
        name = bundle_file(kind, self.arch)
        self.builder.execute("tar", *TAR_FLAGS, "-czf", "/root/" + name, "-C", source, ".")
        self.builder.pull("/root/" + name, self.output / name)
        return {"file": name, "sha256": sha256(self.output / name)}

    def deb_bundle(self, kind, packages):
        """Download packages plus the dependencies the base image lacks; the
        archive holds .deb files only, installed offline with dpkg later."""
        self.builder.execute("rm", "-rf", DEBS)
        self.builder.execute("mkdir", "-p", DEBS)
        self.builder.execute("apt-get", "-o", f"Dir::Cache::archives={DEBS}", "install", "--download-only", "-y",
                             "--no-install-recommends", *packages)
        self.builder.execute("sh", "-c", f"rm -rf {DEBS}/partial {DEBS}/lock")
        details = self.archive(kind, DEBS)
        details["packages"] = deb_inventory(self.builder.output("ls", DEBS))
        return details

    def docker_repository(self):
        execute = self.builder.execute
        execute("apt-get", "install", "-y", "--no-install-recommends", "ca-certificates", "curl")
        execute("install", "-d", "-m", "0755", "/etc/apt/keyrings")
        execute("curl", "--fail", "--silent", "--show-error", "--location", "--retry", "3",
                DOCKER_KEY, "-o", "/etc/apt/keyrings/docker.asc")
        arch = self.builder.output("dpkg", "--print-architecture").strip()
        execute("sh", "-c", f"printf 'deb [arch={arch} signed-by=/etc/apt/keyrings/docker.asc] "
                "https://download.docker.com/linux/ubuntu noble stable\n' >/etc/apt/sources.list.d/docker.list")
        execute("apt-get", "-o", "APT::Update::Error-Mode=any", "-o", "Acquire::Retries=3", "update")

    def playwright_bundle(self):
        self.builder.execute("mkdir", "-p", "/root/bundle-src")
        for name in ("tool_download.py", "tool_lock.py", "build-playwright-bundle.py",
                     "mcp/package.json", "mcp/package-lock.json"):
            self.builder.push(ROOT / "infra/image" / name, "/root/bundle-src/" + Path(name).name)
        # The selected lock, so --lock-file governs Node here as it does in the image build.
        self.builder.push(self.lock_file, "/root/bundle-src/tools.lock.json")
        self.builder.execute("apt-get", "install", "-y", "--no-install-recommends", "ca-certificates", "python3")
        self.builder.execute("python3", "/root/bundle-src/build-playwright-bundle.py")
        details = self.archive("playwright", "/root/playwright-bundle")
        details.update(json.loads(self.builder.output("cat", "/root/playwright-bundle/bundle.json")))
        return details

    def pi_bundle(self):
        """Pi and the pairtask extension (#155); build-pi-bundle.py prunes
        examples, checks every license and writes the notices."""
        self.builder.execute("mkdir", "-p", "/root/bundle-src/pi")
        for name in ("tool_download.py", "tool_lock.py", "build-pi-bundle.py"):
            self.builder.push(ROOT / "infra/image" / name, "/root/bundle-src/" + name)
        for name in ("package.json", "package-lock.json"):
            self.builder.push(ROOT / "infra/image/pi" / name, "/root/bundle-src/pi/" + name)
        self.builder.push(ROOT / "integrations/pi/index.ts", "/root/bundle-src/pi-extension-source.ts")
        self.builder.push(self.lock_file, "/root/bundle-src/tools.lock.json")
        self.builder.execute("apt-get", "install", "-y", "--no-install-recommends", "ca-certificates", "python3")
        self.builder.execute("python3", "/root/bundle-src/build-pi-bundle.py")
        details = self.archive("pi", "/root/pi-bundle")
        details.update(json.loads(self.builder.output("cat", "/root/pi-bundle/bundle.json")))
        return details

    def build(self, kinds=BUNDLE_KINDS):
        """Build the requested bundles and write their manifest next to them."""
        self.builder.execute("apt-get", "-o", "APT::Update::Error-Mode=any", "-o", "Acquire::Retries=3", "update")
        bundles = {}
        if "playwright" in kinds:
            bundles["playwright"] = self.playwright_bundle()
        if "chromium-deps" in kinds:
            bundles["chromium-deps"] = self.deb_bundle("chromium-deps", CHROMIUM_LIBS + CHROMIUM_FONTS)
        if "docker" in kinds:
            self.docker_repository()
            bundles["docker"] = self.deb_bundle("docker", docker_packages() + DOCKER_SUPPORT)
        if "pi" in kinds:
            bundles["pi"] = self.pi_bundle()
        manifest = {"architecture": self.arch, "bundles": bundles}
        (self.output / manifest_file(self.arch)).write_text(json.dumps(manifest, indent=2) + "\n")
        return manifest


def build_bundles(builder, arch, output, kinds=BUNDLE_KINDS):
    return BundleBuild(builder, arch, output).build(kinds)
