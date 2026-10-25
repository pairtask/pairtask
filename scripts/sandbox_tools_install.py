"""Install tool generations into the containers from the host, at rollout.

The host is the only installer: it caches each artifact by digest, pushes only
what a container's store lacks, and runs the in-container activator on a
fresh, unreleased root. See docs/sandbox-tools.md.
"""

from __future__ import annotations

from dataclasses import dataclass, replace
import hashlib
import json
import shlex
from pathlib import Path
import subprocess
import sys
import tempfile

from sandbox_artifacts import ARCHITECTURES, bundles_name
from sandbox_layout import discover
from sandbox_tools import CACHE, cache_artifact, resolve

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT / "infra/image"))
from tool_store import generation_id, manifest_for  # noqa: E402

INCOMING = "/var/lib/collab-ai-tools/incoming"
AGENT_KEY = "/etc/ssh/authorized_keys/agent"
# Every launch mode needs these; the rest can be turned off per preset.
REQUIRED_TOOLS = ("go", "codex", "claude", "rtk", "starship")
OPTIONAL_TOOLS = ("cs-mcp", "playwright", "docker", "pi")
# Optional tools delivered as release bundles rather than lock artifacts. They
# are installed into store-mode roots only; a baked root already has them.
BUNDLE_TOOLS = ("playwright", "docker", "pi")
PLAYWRIGHT_ENTRY = "node_modules/@playwright/mcp/cli.js"
PI_ENTRY = "node_modules/@earendil-works/pi-coding-agent/package.json"
# Bundle-backed tools that run on the lock's Node.
NODE_TOOLS = ("playwright", "pi")
# The release bundles each bundle-backed tool consists of: (bundle, kind, entry).
BUNDLE_PARTS = {
    "playwright": (("playwright", "archive", PLAYWRIGHT_ENTRY), ("chromium-deps", "debs", ".")),
    "docker": (("docker", "debs", "."),),
    "pi": (("pi", "archive", PI_ENTRY),),
}
SECURED_TOOLS = ("codex", "claude")
PROVENANCE_KEYS = ("collab", "recipe_commit", "files_sha256")
# Units (layout keys) that wait for tools readiness; started by the host once a root is released.
GATED_UNITS = {
    "workspace": ("tools_ready", "rtk_setup", "docker_session", "docker_setup"),
    "secured": ("tools_ready",),
}
RETRY_COMMAND = "python3 scripts/sandbox-provision.py tools-retry"


class Container:
    """One managed instance, driven through the authenticated Incus endpoint."""

    def __init__(self, remote, project, name, run=subprocess.run):
        self.name = name
        self.role = name  # a disposable test instance can play a role under another name (as_role)
        self.command = ["incus", "--project", project]
        self.target = f"{remote}:{name}"
        self.run = run
        self._layout = None

    @property
    def layout(self):
        """The image layout, probed once for this container."""
        if self._layout is None:
            self._layout = discover(self)
        return self._layout

    def as_role(self, role):
        self.role = role
        return self

    def execute(self, *args, **kwargs):
        return self.run(self.command + ["exec", self.target, "-T", "--", *args], check=True, **kwargs)

    def output(self, *args):
        return self.execute(*args, capture_output=True, text=True).stdout

    def push(self, source, destination):
        self.run(self.command + ["file", "push", str(source), self.target + destination], check=True)


def selection(enabled):
    """The tool selection as {name: bool}; required tools are always on."""
    unknown = set(enabled) - set(REQUIRED_TOOLS) - set(OPTIONAL_TOOLS)
    if unknown:
        raise ValueError(f"Unknown tools: {', '.join(sorted(unknown))}")
    return {**{name: True for name in REQUIRED_TOOLS}, **{name: name in enabled for name in OPTIONAL_TOOLS}}


def tools_for(role, selected, store_mode=False):
    """Lock artifacts for a role. Bundle-backed tools are separate (see
    bundle_entries); Node travels with Playwright or Pi in store mode."""
    if role == "secured":
        return list(SECURED_TOOLS)
    tools = [name for name in (*REQUIRED_TOOLS, *OPTIONAL_TOOLS) if selected[name] and name not in BUNDLE_TOOLS]
    if store_mode and any(selected[name] for name in NODE_TOOLS):
        tools.append("node")
    return tools


def bundle_entries(release, selected, role, store_mode):
    """Manifest entries for the selected bundle-backed tools, from the
    release's bundle manifest. Only a store-mode workspace installs them."""
    parts = bundle_parts(selected) if role == "workspace" and store_mode else []
    if not parts:
        return {}
    if release.bundles is None:
        wanted = ", ".join(tool for tool in BUNDLE_TOOLS if selected[tool])
        raise ValueError(f"the selected tools need release bundles ({wanted}) that this release does not carry")
    bundles = release.bundles["manifest"]["bundles"]
    missing = sorted({name for name, _, _ in parts if name not in bundles})
    if missing:
        raise ValueError(f"this release has no {', '.join(missing)} bundle for the selected tools; "
                         "deselect them or roll out a newer release")
    return {name: bundle_entry(bundles[name], kind, entry) for name, kind, entry in parts}


def shipped_tools(release):
    """The optional tools a release can install: every lock-backed one, and a
    bundle-backed one only when the release carries all of its bundles."""
    bundles = release.bundles["manifest"]["bundles"] if release and release.bundles else {}
    return [tool for tool in OPTIONAL_TOOLS
            if tool not in BUNDLE_TOOLS or all(name in bundles for name, _, _ in BUNDLE_PARTS[tool])]


def bundle_parts(selected):
    """(bundle, kind, entry) for every bundle the selected tools consist of."""
    return [part for tool in BUNDLE_TOOLS if selected[tool] for part in BUNDLE_PARTS[tool]]


def bundle_entry(details, kind, entry):
    item = {"kind": kind, "entry": entry, "sha256": details["sha256"], "file": details["file"],
            "version": details.get("playwright_mcp") or details.get("pi_coding_agent") or details["sha256"][:12]}
    if kind == "debs":
        item["packages"] = details["packages"]
    return item


def digest(data):
    return hashlib.sha256(data).hexdigest()


def status(container):
    return json.loads(container.output(container.layout.tools_activate, "status"))


# The probe exits 0 whenever it evaluated and names its verdict; the Incus
# client also exits 1 on its own errors, so an exit code alone can't be trusted.
PROBE_SCRIPT = 'if "$@"; then echo present; else echo absent; fi'


def probe(container, *test):
    """A `test` inside the container. Only an explicit verdict counts; any
    other exit code or output is an error, never proof of absence."""
    result = container.run(container.command + ["exec", container.target, "-T", "--", "sh", "-c", PROBE_SCRIPT,
                                                "probe", "test", *test], capture_output=True, text=True)
    verdict = (result.stdout or "").strip()
    if result.returncode != 0 or verdict not in ("present", "absent"):
        raise ValueError(f"{container.name}: could not probe {' '.join(test)} "
                         f"(exit {result.returncode}, output {verdict!r})")
    return verdict == "present"


def agent_key_installed(container):
    """Whether the host ever admitted an agent to this root."""
    return probe(container, "-e", AGENT_KEY)


def has_tool_store(container):
    """Whether this root's image carries the activator: the evidence that the
    release supports the tool store. Older releases keep their baked tools."""
    present = probe(container, "-x", container.layout.tools_activate)
    if not present and container.layout.version >= 2:
        raise ValueError(f"{container.name}: this image's layout requires {container.layout.tools_activate}")
    return present


def never_released(container, state):
    """True only for a root no agent could have used: no ready state, and the
    host never installed the agent's SSH key."""
    if state is not None and state.get("state") != "maintenance":
        return False  # ready, or a state this host doesn't understand: not safe to mutate
    return not agent_key_installed(container)


@dataclass(frozen=True)
class Release:
    """What the host knows about the selected release: the server architecture
    and, when the image directory carries a manifest, the release's lock and
    build provenance. Without a manifest the root's own lock governs.
    `bundles` is the release's bundle manifest with the base its files come from."""
    lock: dict | None
    machine: str
    provenance: dict | None = None
    bundles: dict | None = None


def load_bundles(image_dir, machine):
    """The bundle manifest and source recorded next to the image, if any."""
    manifest_path = Path(image_dir) / bundles_name(machine)
    source = Path(image_dir) / "bundle-source.json"
    if not manifest_path.exists() or not source.exists():
        return None
    base = json.loads(source.read_text()).get("base")
    if not base:
        return None
    return {"manifest": json.loads(manifest_path.read_text()), "base": base}


# Prints exactly one verdict line; any other output, or a failed exec, is an error.
MODE_SCRIPT = 'if [ -e {0} ]; then printf "mode:%s\\n" "$(cat {0})"; else echo mode:absent; fi'
MODES = {"mode:store": True, "mode:baked": False, "mode:absent": False}


def store_mode(container):
    """Whether this root's launchers depend on the store (tools-mode = store).
    Only a confirmed absent file means an older, baked image; a transport error
    or an unknown mode fails closed rather than omitting tools."""
    mode_file = shlex.quote(f"{container.layout.share_dir}/tools-mode")
    verdict = container.output("sh", "-c", MODE_SCRIPT.format(mode_file)).strip()
    if verdict not in MODES:
        raise ValueError(f"{container.name}: unknown tools mode ({verdict!r})")
    if verdict == "mode:absent" and container.layout.version >= 2:
        raise ValueError(f"{container.name}: this image's layout requires a tools mode file")
    return MODES[verdict]


def build_manifest(container, release, selected):
    """The manifest for this container's root: digests of the root's own lock
    and build source, which must agree with the release."""
    share = container.layout.share_dir
    lock_text = container.output("cat", f"{share}/tools.lock.json")
    source_text = container.output("cat", f"{share}/build-source.json")
    lock = json.loads(lock_text)
    if release.lock is not None and lock["artifacts"] != release.lock["artifacts"]:
        raise ValueError(f"{container.name}: the image's tool lock differs from the release manifest")
    if release.provenance is not None and json.loads(source_text) != release.provenance:
        raise ValueError(f"{container.name}: the root's build source differs from the release manifest")
    facts = {"release": digest(source_text.encode()), "lock_sha256": digest(lock_text.encode()),
             "architecture": release.machine}
    mode = store_mode(container)
    manifest = manifest_for(lock, facts, selected, tools_for(container.role, selected, mode))
    manifest["tools"].update(bundle_entries(release, selected, container.role, mode))
    return lock, manifest


def release_units(container):
    """Start the gated entry points after a successful release. Idempotent: in
    baked mode they are already running; in store mode their first boot failed
    on the readiness gate and they need a reset."""
    units = [container.layout.units[key] for key in GATED_UNITS.get(container.role, GATED_UNITS["secured"])]
    container.execute("systemctl", "reset-failed", *units)
    container.execute("systemctl", "start", *units)


def install(container, release, selected, cache=CACHE):
    """Activate the generation for container; returns the generation ID, or
    None when the released root already runs exactly this generation. Either
    way the gated units are (re)started, so an interrupted release completes."""
    lock, manifest = build_manifest(container, release, selected)
    release = replace(release, lock=lock)  # artifacts resolve from the root's verified lock
    generation = generation_id(manifest)
    current = status(container)
    if already_released(container, current, generation):
        return None
    require_fresh_root(container, current.get("state"))
    complete = current.get("complete", [])
    deliver(container, release, [(name, tool) for name, tool in manifest["tools"].items()
                                 if tool["sha256"] not in complete], cache)
    activate(container, manifest, selected)
    verify_ready(container, generation)
    release_units(container)
    return generation


def already_released(container, current, generation):
    """A ready root is verified, never changed: the same generation is complete
    (its units are started), any other generation needs a rollout."""
    state = current.get("state")
    if state is None or state.get("state") != "ready":
        return False
    if current.get("current") != generation:
        raise ValueError(f"{container.name} already runs a released tool generation; "
                         "changing tools needs a rollout (a new root)")
    release_units(container)
    return True


def require_fresh_root(container, state):
    """Every mutation requires a root no agent could have used: no ready or
    unknown state, and no agent key."""
    if not never_released(container, state):
        raise ValueError(f"{container.name} was released to agents or is in an unknown tool state; "
                         "run rollout to replace the root")


def deliver(container, release, pending, cache):
    """Push the artifacts the store lacks, from the verified host cache."""
    container.execute("install", "-d", "-m", "0700", INCOMING)
    for name, tool in pending:
        print(f"{container.name}: installing {name} {tool['version']}", flush=True)
        container.push(cache_artifact(artifact_item(release, name, tool), cache), f"{INCOMING}/{tool['file']}")


def artifact_item(release, name, tool):
    """Where to fetch one manifest entry: a lock artifact from its vendor URL,
    a bundle from the release's recorded bundle source."""
    if name in release.lock["artifacts"]:
        return resolve(release.lock, release.machine, [name])[0]
    return {"name": name, "version": tool["version"], "kind": tool["kind"], "entry": tool["entry"],
            "url": release.bundles["base"] + tool["file"], "sha256": tool["sha256"]}


def activate(container, manifest, selected):
    with tempfile.TemporaryDirectory(prefix="collab-tools-") as directory:
        for name, value in (("manifest.json", manifest), ("tools.json", selected)):
            path = Path(directory) / name
            path.write_text(json.dumps(value, indent=2, sort_keys=True) + "\n")
            container.push(path, f"{INCOMING}/{name}")
    layout = container.layout
    container.execute("install", "-d", "-m", "0755", layout.etc_dir)
    container.execute("install", "-m", "0644", f"{INCOMING}/tools.json", f"{layout.etc_dir}/tools.json")
    container.execute(layout.tools_activate, "activate", f"{INCOMING}/manifest.json", "--incoming", INCOMING)
    container.execute("rm", "-rf", INCOMING)


def ready_for(current, generation):
    """Whether a status report says ready for exactly this generation."""
    state = current.get("state") or {}
    return state.get("state") == "ready" and current.get("current") == generation


def verify_ready(container, generation):
    if not ready_for(status(container), generation):
        raise ValueError(f"{container.name}: tool activation did not reach ready for {generation}")


def release_lock_from(image_dir):
    """The lock the release was built with, from the image manifest."""
    return json.loads((Path(image_dir) / "manifest.json").read_text())["tools"]


def release_provenance(image_dir):
    """(provenance, architecture) of the release image, for the root to match;
    (None, None) when the image directory carries no manifest."""
    path = Path(image_dir) / "manifest.json"
    if not path.exists():
        return None, None
    manifest = json.loads(path.read_text())
    return provenance_of(manifest), manifest.get("architecture")


def supports_tool_store(release_lock):
    """A lock without the artifact table can't describe a generation; such a
    release predates the store. The converse is decided per root by
    has_tool_store(), because the table shipped before the activator."""
    return "artifacts" in release_lock


def provenance_of(manifest):
    return {key: manifest[key] for key in PROVENANCE_KEYS if key in manifest}
