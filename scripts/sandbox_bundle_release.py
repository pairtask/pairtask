"""Optional tool bundles in long-lived `bundles-<digest>` releases.

The digest covers everything that shapes a bundle: the recipe files, the
effective Node pin, the base image pin, the architecture and the bundle kinds.
CI reuses a published bundle release whose inputs match, and publishes a newly
built one once; workspace releases then only reference it. Apt package sets
resolve when a bundle is first built and stay frozen until an input changes.
"""

import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
from urllib.parse import quote

from sandbox_artifacts import ARCHITECTURES, sha256, validate_bundles
from sandbox_base_image import IntegrityError, Unavailable, fetch, get_object, release_shape
from sandbox_base_mirror import find_tagged
from sandbox_bundles import BUNDLE_KINDS, bundle_file, manifest_file
from sandbox_releases import repository


ROOT = Path(__file__).resolve().parents[1]
INPUT_FILES = (
    "scripts/sandbox_bundles.py", "scripts/sandbox-bundles.py", "scripts/sandbox_builder.py",
    "infra/image/build-playwright-bundle.py", "infra/image/tool_download.py", "infra/image/tool_lock.py",
    "infra/image/mcp/package.json", "infra/image/mcp/package-lock.json", "infra/image/docker-packages.txt",
    "infra/image/build-pi-bundle.py", "infra/image/pi/package.json", "infra/image/pi/package-lock.json",
    "integrations/pi/index.ts",
)
SCHEMA = 1


def run(args, **kwargs):
    return subprocess.run(args, check=True, **kwargs)


def checked_kinds(kinds):
    kinds = list(kinds)
    unknown = [kind for kind in kinds if kind not in BUNDLE_KINDS]
    if unknown or len(set(kinds)) != len(kinds):
        raise ValueError(f"Bundle kinds must be distinct and among {', '.join(BUNDLE_KINDS)}: {kinds}")
    return sorted(kinds)


def bundle_inputs(lock, architecture, kinds=BUNDLE_KINDS, root=ROOT):
    """The canonical description of everything one architecture's bundles are built from."""
    node = lock["artifacts"]["node"]
    return {
        "schema": SCHEMA,
        "architecture": ARCHITECTURES[architecture],
        "base_image": lock["architectures"][architecture]["base_image"],
        "kinds": checked_kinds(kinds),
        "node": {"version": node["version"], "kind": node["kind"], "url": node["url"], "entry": node["entry"],
                 **node["architectures"][architecture]},
        "files": {path: sha256(Path(root) / path) for path in INPUT_FILES},
    }


def canonical(inputs):
    return json.dumps(inputs, sort_keys=True, separators=(",", ":"))


def inputs_digest(inputs):
    return hashlib.sha256(canonical(inputs).encode()).hexdigest()


def release_name(digest):
    return "bundles-" + digest


def inputs_file(arch):
    return f"collab-ai-bundle-inputs-linux-{arch}.json"


def expected_assets(inputs):
    arch = inputs["architecture"]
    return {manifest_file(arch), inputs_file(arch), *(bundle_file(kind, arch) for kind in inputs["kinds"])}


def published_bundles(repo, inputs):
    """The published bundle release for inputs, or None while there is none
    (or only a draft). A published release must hold exactly the expected assets."""
    name = release_name(inputs_digest(inputs))
    release = get_object(f"https://api.github.com/repos/{repository(repo)}/releases/tags/{quote(name, safe='')}")
    if release is None:
        return None
    if release.get("tag_name") != name:
        raise IntegrityError(f"Release lookup for {name} returned {release.get('tag_name')}")
    release_shape(release, ("name",))
    if release["draft"]:
        return None
    exact_assets(release, inputs)
    return release_shape(release, ("name", "browser_download_url"))


def exact_assets(release, inputs):
    names = [asset["name"] for asset in release["assets"]]
    if len(set(names)) != len(names) or set(names) != expected_assets(inputs):
        raise IntegrityError(f"Release {release['tag_name']} must hold exactly {sorted(expected_assets(inputs))}; "
                             f"it has {sorted(names)}")


def verify_payload(directory, inputs):
    """Check a complete local payload against inputs: the inputs file, the
    manifest's binding to the release, and every bundle digest."""
    directory = Path(directory)
    check_inputs_file(directory, inputs)
    manifest = bound_manifest(directory, inputs)
    for kind, details in manifest["bundles"].items():
        check_bundle(directory, inputs["architecture"], kind, details)
    return manifest


def check_inputs_file(directory, inputs):
    arch = inputs["architecture"]
    recorded = json.loads((directory / inputs_file(arch)).read_text())
    if recorded != inputs or inputs_digest(recorded) != inputs_digest(inputs):
        raise IntegrityError(f"{inputs_file(arch)} doesn't describe these bundle inputs")


def bound_manifest(directory, inputs):
    """The payload's manifest, valid and bound to the release of inputs."""
    arch, name = inputs["architecture"], release_name(inputs_digest(inputs))
    manifest = json.loads((directory / manifest_file(arch)).read_text())
    validate_bundles(manifest, machine(arch))
    if manifest.get("release") != name or sorted(manifest["bundles"]) != inputs["kinds"]:
        raise IntegrityError(f"{manifest_file(arch)} isn't the manifest of {name}")
    return manifest


def check_bundle(directory, arch, kind, details):
    if details["file"] != bundle_file(kind, arch):
        raise IntegrityError(f"Bundle {kind} has an unexpected file name in {manifest_file(arch)}")
    if sha256(directory / details["file"]) != details["sha256"]:
        raise IntegrityError(f"Bundle {kind} doesn't match {manifest_file(arch)}")


def machine(arch):
    return next(machine for machine, label in ARCHITECTURES.items() if label == arch)


def reuse(repo, inputs, output):
    """Copy the published bundles for inputs into output once all of them
    verify; None when no such release is published yet."""
    release = published_bundles(repo, inputs)
    if release is None:
        return None
    output = Path(output)
    staging = Path(tempfile.mkdtemp(prefix=".bundles-", dir=output))
    try:
        for asset in release["assets"]:
            fetch(asset["browser_download_url"], staging / asset["name"])
        manifest = verify_payload(staging, inputs)
        place_all(staging, output, sorted(expected_assets(inputs)))
    finally:
        shutil.rmtree(staging, ignore_errors=True)
    return manifest


def place_all(staging, output, names):
    """Move verified files into output, never replacing an existing one. On any
    failure the files this call placed are removed again; others are kept."""
    placed = []
    try:
        for name in names:
            os.link(staging / name, output / name)  # fails if the target exists
            placed.append(output / name)
    except BaseException:
        for path in placed:
            path.unlink(missing_ok=True)
        raise


def write_release_files(output, inputs):
    """Bind a freshly built manifest to its release and record the inputs."""
    output, arch = Path(output), inputs["architecture"]
    path = output / manifest_file(arch)
    manifest = json.loads(path.read_text())
    manifest["release"] = release_name(inputs_digest(inputs))
    path.write_text(json.dumps(manifest, indent=2) + "\n")
    (output / inputs_file(arch)).write_text(json.dumps(inputs, indent=2, sort_keys=True) + "\n")
    return manifest


def local_inputs(directory, architecture):
    """The inputs a CI payload directory records, checked for shape."""
    arch = ARCHITECTURES[architecture]
    inputs = json.loads((Path(directory) / inputs_file(arch)).read_text())
    if not isinstance(inputs, dict):
        raise IntegrityError(f"{inputs_file(arch)} isn't a bundle inputs record")
    if (inputs.get("schema"), inputs.get("architecture")) != (SCHEMA, arch):
        raise IntegrityError(f"{inputs_file(arch)} isn't a schema {SCHEMA} bundle inputs record for {architecture}")
    checked_kinds(inputs.get("kinds", []))
    return inputs


def publish(repo, directory, architecture):
    """Make sure the bundles a CI run tested are the published bundles of
    their inputs. A published release is only ever compared, never changed."""
    inputs = local_inputs(directory, architecture)
    verify_payload(directory, inputs)
    name = release_name(inputs_digest(inputs))
    release = find_tagged(repo, name)
    if release is not None and not release["draft"]:
        return compare_published(repo, release, inputs, directory)
    if release is not None:
        # Our own staging from an interrupted run; the tested payload is verified above.
        run(["gh", "api", "-X", "DELETE", f"repos/{repository(repo)}/releases/{release['id']}"])
    stage(repo, inputs, directory)
    if published_bundles(repo, inputs) is None:
        raise Unavailable(f"Release {name} didn't publish")
    print(f"Bundles {name[8:20]} ({architecture}): published", flush=True)
    return "published"


def compare_published(repo, release, inputs, directory):
    """An already published release must hold byte-identical files."""
    exact_assets(release, inputs)
    with tempfile.TemporaryDirectory(prefix="collab-bundles-") as temporary:
        same_bytes(pull_all(repo, release, Path(temporary)), directory, inputs, "already holds different")
    print(f"Bundles {release['tag_name'][8:20]} ({inputs['architecture']}): published release verified", flush=True)
    return "verified"


def same_bytes(pulled, tested, inputs, problem):
    """Every asset of a release must be byte-identical to the tested payload."""
    name = release_name(inputs_digest(inputs))
    for asset in sorted(expected_assets(inputs)):
        if sha256(Path(pulled) / asset) != sha256(Path(tested) / asset):
            raise IntegrityError(f"{name} {problem} {asset}; this run's tested bundles can't be released")


def pull_all(repo, release, directory):
    for asset in release["assets"]:
        with (directory / asset["name"]).open("xb") as output:
            run(["gh", "api", "-H", "Accept: application/octet-stream",
                 f"repos/{repository(repo)}/releases/assets/{asset['id']}"], stdout=output)
    return directory


def stage(repo, inputs, directory):
    """Create a draft, upload the tested payload, verify it as uploaded, publish."""
    name, arch = release_name(inputs_digest(inputs)), inputs["architecture"]
    files = [str(Path(directory) / asset) for asset in sorted(expected_assets(inputs))]
    run(["gh", "release", "create", name, "--repo", repo, "--draft", "--latest=false",
         "--title", f"Tool bundles {name[8:20]} ({arch})", "--notes", notes(inputs), *files])
    draft = find_tagged(repo, name)
    if draft is None or not draft["draft"]:
        raise Unavailable(f"Draft {name} is missing after upload")
    exact_assets(draft, inputs)
    with tempfile.TemporaryDirectory(prefix="collab-bundles-") as temporary:
        same_bytes(pull_all(repo, draft, Path(temporary)), directory, inputs, "was uploaded with different")
    run(["gh", "release", "edit", name, "--repo", repo, "--draft=false", "--latest=false"])


def notes(inputs):
    return (f"Input bundles, not a workspace release: the optional tool bundles for linux-{inputs['architecture']}, "
            f"built from the inputs in {inputs_file(inputs['architecture'])}. Workspace releases reference it "
            "from their bundle manifest; keep it while any of them does.")
