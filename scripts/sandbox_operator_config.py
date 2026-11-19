"""Discover one host deployment and package only its provisioning inputs."""

import hashlib
import json
from pathlib import Path
import subprocess

from sandbox_env import canonical_name, lookup
from sandbox_operator_state import deployment_lock, private_write


ROOT = Path(__file__).resolve().parents[1]
def operator_home():
    """The operator registry directory: an absolute path after ~ expansion.
    Unset (or an empty legacy value) means the default; an empty or relative
    canonical value is an error, never the current directory."""
    value, present, legacy_used = lookup("OPERATOR_HOME")
    if not present or (legacy_used and value == ""):
        return Path.home() / ".local/state/pairtask/operator"
    home = Path(value).expanduser()
    if value == "" or not home.is_absolute():
        raise ValueError(f"{canonical_name('OPERATOR_HOME')} must be an absolute path")
    return home


OPERATOR_HOME = operator_home()
LOCATION = OPERATOR_HOME / "locations.json"


def state_project(directory):
    path = directory / "terraform.tfstate"
    if path.is_symlink():
        raise ValueError(f"Refusing symlinked state: {path}")
    if not path.exists():
        return None
    state = json.loads(path.read_text())
    projects = set(managed_projects(state))
    if not projects:
        return None
    if len(projects) != 1:
        raise ValueError(f"Expected exactly one managed Incus project in {path}")
    return projects.pop()


def managed_projects(state):
    for resource in state.get("resources", []):
        if resource["type"] == "incus_project":
            yield from (item["attributes"]["name"] for item in resource.get("instances", []))


def deployment_directory(explicit, project, root=ROOT, location=LOCATION, create=True):
    directory = explicit if explicit else discover_directory(project, root, location, create=create)
    if directory is None:
        return None
    if directory.is_symlink() or not directory.is_dir():
        raise ValueError("State directory must be an existing, non-symlinked provisioning directory")
    directory = directory.resolve()
    actual = state_project(directory)
    if actual and actual != project:
        raise ValueError(f"State belongs to {actual}, not {project}")
    return directory


def discover_directory(project, root, location, create=True):
    saved = read_registry(location)
    if project in saved:
        return Path(saved[project])
    candidates = worktree_directories(root)
    matches = [path for path in candidates if state_project(path) == project]
    if len(matches) > 1:
        raise ValueError("Multiple deployment states found; select one once with --state-dir")
    if matches:
        return matches[0]
    return new_deployment(root, location.parent / project) if create else None


def worktree_directories(root):
    result = subprocess.run(["git", "worktree", "list", "--porcelain"], cwd=root,
                            check=True, capture_output=True, text=True)
    return [Path(line.removeprefix("worktree ")) / "infra/incus"
            for line in result.stdout.splitlines() if line.startswith("worktree ")]


def read_registry(location):
    return json.loads(location.read_text()) if location.exists() else {}


def new_deployment(root, destination):
    if destination.exists():
        raise ValueError(f"Unregistered operator directory already exists: {destination}; select it with --state-dir")
    destination.mkdir(mode=0o700, parents=True)
    for name, content in provisioning_files(root / "infra/incus").items():
        if name.endswith((".tf", ".tf.json", ".py")) or name == ".terraform.lock.hcl":
            private_write(destination / name, content)
    return destination


def remember(directory, project, location=LOCATION):
    location.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
    with deployment_lock(location.parent):
        saved = read_registry(location)
        saved[project] = str(directory)
        private_write(location, (json.dumps(saved, indent=2) + "\n").encode())


def provisioning_files(directory):
    names = {".terraform.lock.hcl", "secured_preflight.py"}
    for pattern in ("*.tf", "*.tf.json", "*.auto.tfvars", "*.auto.tfvars.json", "terraform.tfvars", "terraform.tfvars.json"):
        names.update(path.name for path in directory.glob(pattern))
    names.discard("operator-backend.tf.json")
    files = {}
    for name in sorted(names):
        path = directory / name
        if path.is_symlink() or not path.is_file():
            raise ValueError(f"Expected a regular provisioning file: {path}")
        files[name] = path.read_bytes()
    return files


def image_selection(directory, requested):
    path = image_variables(directory, requested)
    data = read_image_variables(path)
    image = Path(data["image_file"])
    if image.is_symlink() or not image.is_absolute():
        raise ValueError("Image must be an absolute, non-symlinked file")
    digest = file_sha(image)
    if digest != data["image_fingerprint"]:
        raise ValueError("Workspace image checksum mismatch")
    return image, digest, path


def read_image_variables(path):
    if not path.is_file():
        raise ValueError(f"No verified image at {path.parent}. Run sandbox-download.py first, "
                         "then pass its printed image directory with --image-dir.")
    return json.loads(path.read_text())


def image_variables(directory, requested):
    if requested:
        return (requested / "image.tfvars.json").resolve()
    saved = directory / "operator-image.json"
    if saved.exists():
        return Path(json.loads(saved.read_text())["variables"])
    return existing_image_variables(directory)


def existing_image_variables(directory):
    choices = [candidate for candidate in directory.glob("*.auto.tfvars.json")
               if contains_image(candidate)]
    if len(choices) != 1:
        raise ValueError("Run sandbox-provision.py rollout, or select a verified image with --image-dir")
    return choices[0]


def contains_image(path):
    data = json.loads(path.read_text())
    return bool(data.get("image_file") and data.get("image_fingerprint"))


def file_sha(path):
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def plan_identity(files, fingerprint, server, options):
    return {"files": {name: hashlib.sha256(data).hexdigest() for name, data in files.items()},
            "image": fingerprint, "server": server, "options": options}
