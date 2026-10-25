"""Native image release files and checksum-verified local downloads."""

from collections import namedtuple
import hashlib
import json
from pathlib import Path
import re
import shutil
import urllib.error
import zipfile

from sandbox_https import urlopen
from sandbox_versions import fingerprint


ARCHITECTURES = {"aarch64": "arm64", "x86_64": "amd64"}


class MissingAsset(FileNotFoundError, ValueError):
    """An expected asset is not in the given source: a usage error for the
    image, and confirmed absence for the optional bundle manifest."""


def sha256(path):
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        while chunk := stream.read(1024 * 1024):
            digest.update(chunk)
    return digest.hexdigest()


def asset_name(architecture):
    return f"collab-ai-workspace-linux-{ARCHITECTURES[architecture]}"


def image_variables(directory, digest, image_directory=None):
    image = (image_directory or directory) / "workspace.tar.gz"
    values = {"image_file": str(image.resolve()), "image_fingerprint": digest}
    (directory / "image.tfvars.json").write_text(json.dumps(values, indent=2) + "\n")


def release_assets(directory, manifest):
    destination = directory / "release"
    destination.mkdir()
    name = asset_name(manifest["architecture"])
    shutil.copyfile(directory / "workspace.tar.gz", destination / f"{name}.tar.gz")
    (destination / f"{name}.json").write_text(json.dumps(manifest, indent=2) + "\n")


def fetch(url, destination):
    with urlopen(url, timeout=120) as response, destination.open("xb") as output:
        shutil.copyfileobj(response, output, length=1024 * 1024)


def from_zip(archive, name, destination):
    # Copy only the two exact expected assets; never extract archive paths.
    with zipfile.ZipFile(archive) as bundle:
        matches = [item for item in bundle.infolist() if item.filename == name]
        if not matches:
            raise MissingAsset(f"{name} is not in {archive}; select the matching architecture ZIP")
        if len(matches) > 1:
            raise ValueError(f"Expected exactly one {name} in {archive}; select the matching architecture ZIP")
        with bundle.open(matches[0]) as source, destination.open("xb") as output:
            shutil.copyfileobj(source, output, length=1024 * 1024)


def install_artifact(output, architecture, obtain):
    manifest = fetch_manifest(output, architecture, obtain)
    fetch_image(output, architecture, manifest, obtain)
    return manifest


def fetch_manifest(output, architecture, obtain):
    """The small manifest names the image fingerprint, so a cached image can be
    recognised before the large download starts."""
    obtain(f"{asset_name(architecture)}.json", output / "manifest.json")
    manifest = json.loads((output / "manifest.json").read_text())
    if manifest["architecture"] != architecture:
        raise ValueError("Image manifest architecture does not match the Incus server")
    fingerprint(manifest["image_fingerprint"])
    return manifest


def fetch_image(output, architecture, manifest, obtain):
    expected = manifest["image_fingerprint"]
    partial = output / "workspace.tar.gz.partial"
    obtain(f"{asset_name(architecture)}.tar.gz", partial)
    if sha256(partial) != expected:
        partial.unlink()
        raise ValueError("Workspace image checksum mismatch; nothing was imported")
    partial.rename(output / "workspace.tar.gz")
    image_variables(output, expected)


def bundles_name(architecture):
    return f"collab-ai-bundles-linux-{ARCHITECTURES[architecture]}.json"


BUNDLE_KINDS = {"playwright": False, "chromium-deps": True, "docker": True, "pi": False}  # name: is a deb set
RESERVED = {"workspace.tar.gz", "manifest.json", "image.tfvars.json", "bundle-source.json"}
BUNDLE_RELEASE = re.compile(r"bundles-[0-9a-f]{64}")
# Where bundles come from: a URL prefix to fetch them from on demand, or None
# to copy them from the image source; repo resolves a manifest's bundle release.
BundleSource = namedtuple("BundleSource", ["base", "repo"], defaults=(None, "pairtask/pairtask"))


def validate_bundles(manifest, architecture):
    """Check a bundle manifest before any file named in it is written: the
    architecture, known bundle names, plain file names that cannot leave the
    image directory or replace its own files, digests and package inventories."""
    if manifest.get("architecture") != ARCHITECTURES[architecture]:
        raise ValueError("bundle manifest is for another architecture")
    if "release" in manifest and not BUNDLE_RELEASE.fullmatch(str(manifest["release"])):
        raise ValueError("bundle manifest names an invalid bundle release")
    for name, details in manifest["bundles"].items():
        validate_bundle(name, details, architecture)


def validate_bundle(name, details, architecture):
    if name not in BUNDLE_KINDS:
        raise ValueError(f"unknown bundle {name!r}")
    safe_file_name(details["file"], architecture)
    if not re.fullmatch(r"[0-9a-f]{64}", details["sha256"]):
        raise ValueError(f"bundle {name} has no SHA256")
    if BUNDLE_KINDS[name] and not has_packages(details):
        raise ValueError(f"bundle {name} has no package inventory")


def has_packages(details):
    packages = details.get("packages")
    return isinstance(packages, dict) and bool(packages)


SAFE_NAME = re.compile(r"[A-Za-z0-9][A-Za-z0-9_-]*(\.[A-Za-z0-9_-]+)*")  # dot-separated parts, never ".."


def safe_file_name(value, architecture):
    if not SAFE_NAME.fullmatch(value or ""):
        raise ValueError(f"unsafe bundle file name {value!r}")
    if value in reserved_names(architecture) or value.startswith((asset_name(architecture), "collab-ai-bundle-inputs-")):
        raise ValueError(f"bundle file name {value!r} is reserved")


def reserved_names(architecture):
    return RESERVED | {bundles_name(architecture)}


def confirmed_absent(error):
    """Only a missing file or an HTTP 404 proves a release has no bundles;
    any other failure must be retried, never remembered."""
    if isinstance(error, urllib.error.HTTPError):
        return error.code == 404
    return isinstance(error, FileNotFoundError)


def fresh(obtain, name, target):
    """Obtain name into target, removing a partial file from an earlier attempt."""
    target.unlink(missing_ok=True)
    try:
        obtain(name, target)
    except BaseException:
        target.unlink(missing_ok=True)
        raise


def fetch_bundles(output, architecture, obtain, origin=BundleSource()):
    """Record where this image's optional tool bundles come from.

    The small bundle manifest is kept next to the image. With a source base (a
    URL prefix the bundle files can be fetched from on demand) nothing else is
    downloaded, and a manifest naming a bundle release is fetched from that
    release instead. Without one, the bundle files are copied here and the base
    becomes this directory; a local source holding none of them falls back to
    the manifest's bundle release. A release without bundles leaves nothing behind."""
    manifest_path = output / bundles_name(architecture)
    source = output / "bundle-source.json"
    try:
        fresh(obtain, bundles_name(architecture), manifest_path)
    except (FileNotFoundError, urllib.error.HTTPError) as error:
        if not confirmed_absent(error):
            raise
        source.write_text(json.dumps({"base": None}, indent=2) + "\n")  # remembered: no bundles
        return None
    manifest = checked_manifest(manifest_path, architecture)
    base = remote_base(manifest, origin) if origin.base is not None else local_base(output, manifest, obtain, origin)
    source.write_text(json.dumps({"base": base}, indent=2) + "\n")
    return manifest


def release_base(repo, release):
    return f"https://github.com/{repo}/releases/download/{release}/"


def remote_base(manifest, origin):
    release = manifest.get("release")
    return release_base(origin.repo, release) if release else origin.base


def local_base(output, manifest, obtain, origin):
    if copy_bundles(output, manifest, obtain):
        return output.resolve().as_uri() + "/"
    if not manifest.get("release"):
        raise ValueError("The bundle files are missing from the image source, and its manifest names no bundle release")
    return release_base(origin.repo, manifest["release"])


def checked_manifest(path, architecture):
    """The downloaded bundle manifest, or ValueError (and no manifest kept)."""
    manifest = json.loads(path.read_text())
    try:
        validate_bundles(manifest, architecture)
    except (ValueError, KeyError, TypeError) as error:
        path.unlink()
        raise ValueError(f"Invalid bundle manifest: {error}") from error
    return manifest


def copy_bundles(output, manifest, obtain):
    """Copy every bundle file next to the image, each verified by digest.
    True when all were copied, False when the source holds none of them; a
    source holding only some, or a corrupt one, is an error."""
    copied = []
    try:
        absent = copy_present(output, manifest, obtain, copied)
        if copied and absent:
            raise ValueError(f"The image source holds only some bundle files; missing: {', '.join(absent)}")
    except BaseException:
        for path in copied:
            path.unlink(missing_ok=True)
        raise
    return not absent


def copy_present(output, manifest, obtain, copied):
    """Copy the bundle files the source has, recording each in copied; the
    names it doesn't have."""
    absent = []
    for details in manifest["bundles"].values():
        if copy_bundle(output, details, obtain):
            copied.append(output / details["file"])
        else:
            absent.append(details["file"])
    return absent


def copy_bundle(output, details, obtain):
    """Copy and verify one bundle file; False if the source doesn't have it."""
    target = output / details["file"]
    try:
        fresh(obtain, details["file"], target)
    except (FileNotFoundError, urllib.error.HTTPError) as error:
        if not confirmed_absent(error):
            raise
        return False
    if sha256(target) != details["sha256"]:
        target.unlink()
        raise ValueError(f"Bundle {details['file']} does not match its digest")
    return True
