"""Which pairtask runs here, which is published, and the one manual command
that would bring it up to date. Nothing here upgrades anything.

The installed app is observed every time (sandbox_version_observe); a
published release counts as installed only when proven to have built that
exact image (sandbox_version_lookup). The step suggested always ends at the
available release, so it is never a downgrade, and it names the deployment
it was observed in.
"""

from collections import namedtuple
from pathlib import Path
import shlex
import subprocess
import sys

from sandbox_remote import default_remote
from sandbox_version_lookup import Lookup, latest_release, installed_release, load_cache, now, parse_build, \
    save_cache, stamp
from sandbox_version_observe import INCUS_TIMEOUT, INSTANCES, OBSERVE_BUDGET, Observer, proof_of

DEFAULT_PROJECT, DEFAULT_REPO = "pairtask", "pairtask/pairtask"
CHAT_BUDGET = 10  # all of the pre-chat observation and publication
OPERATOR_SOCKET = "/run/collab-ai-operator/operator.sock"

# The deployment a report is about.
Scope = namedtuple("Scope", "project remote repo")


def scope_flags(scope):
    """The flags naming a deployment other than the default one."""
    flags = []
    for flag, value, default in (("--project", scope.project, DEFAULT_PROJECT),
                                 ("--remote", scope.remote, default_remote()), ("--repo", scope.repo, DEFAULT_REPO)):
        if value != default:
            flags += [flag, value]
    return flags


# --- the app as observed -------------------------------------------------------

def installed_releases(lookup, cache, instances):
    """Each container's proven release. A stopped container cannot show its
    build source; it shares a running one's release only by the same image."""
    releases = {name: proven_release(lookup, cache, instance) for name, instance in instances.items()}
    by_image = {instances[name]["image"]: tag for name, tag in releases.items() if tag}
    return {name: tag or stopped_release(instances[name], by_image) for name, tag in releases.items()}


def proven_release(lookup, cache, instance):
    proof = proof_of(instance)
    return installed_release(lookup, cache, proof) if proof else None


def stopped_release(instance, by_image):
    return by_image.get(instance["image"]) if instance["status"] != "Running" else None


def app_state(instances, releases):
    """mixed when the containers differ; known when both run the same proven
    release; unknown otherwise, including when either was not seen."""
    images = {i["image"] for i in instances.values()}
    commits = {i["commit"] for i in instances.values() if i["commit"]}
    tags = set(releases.values())
    if len(images) > 1 or len(commits) > 1:
        return "mixed"
    complete = set(instances) == set(INSTANCES) and None not in images
    return "known" if complete and len(tags) == 1 and None not in tags else "unknown"


def app_summary(instances, releases):
    state = app_state(instances, releases)
    commits = {i["commit"] for i in instances.values() if i["commit"]}
    return {"state": state, "release": next(iter(releases.values())) if state == "known" else None,
            "commit": next(iter(commits)) if len(commits) == 1 else None,
            "instances": {name: {k: i[k] for k in ("status", "image", "commit")} for name, i in instances.items()},
            "observed_at": stamp(now()), "members": sorted(t for t in releases.values() if t)}


# --- the step --------------------------------------------------------------------

def decide(app, host, available):
    """The one manual step that brings everything to the available release,
    and only when that step can never be a downgrade. Returns the reported
    state and action, and the step the guidance spells out."""
    target = parse_build(available["release"])
    if target is None:
        return verdict("check_failed", "none")
    members = [parse_build(t) for t in app["members"]]
    host_build = parse_build(host["release"])
    if not comparable(app, members, host_build):
        return verdict("unknown", "none")
    if app["state"] == "mixed":
        return decide_mixed(app, members, host_build, target)
    return decide_single(max(members), host_build, target)


def comparable(app, members, host_build):
    """Every observed release, app and host, is a numbered build."""
    if app["state"] == "unknown" or host_build is None:
        return False
    return bool(members) and None not in members


def decide_mixed(app, members, host_build, target):
    """Containers that differ are reconciled only up to the available
    release, with every member known and none of them newer."""
    complete = len(members) == len(app["instances"]) and max(members) <= target
    return verdict("mixed", "upgrade_app" if complete and host_build >= target else "none")


def decide_single(app_build, host_build, target):
    if app_build > target:
        return verdict("current", "none")  # newer than published: never called outdated
    app_behind = app_build < target
    if host_build < target:
        return verdict("update_available", "update_host", "update_host" if app_behind else "update_host_only")
    return verdict("update_available", "upgrade_app") if app_behind else verdict("current", "none")


def verdict(state, action, step=None):
    """update_host also rolls the app out; update_host_only refreshes the host."""
    return {"state": state, "action": action}, step or action


def guidance(step, scope):
    """The manual command for step, from fixed words and the validated scope.
    The installer only provisions the default deployment, so for any other
    one it refreshes the host alone and the scoped upgrade follows."""
    flags = scope_flags(scope)
    upgrade = shlex.join(["pk", "upgrade"] + flags)
    if step == "upgrade_app":
        return upgrade
    if step not in ("update_host", "update_host_only"):
        return ""
    installer = installer_line(scope.repo)
    if step == "update_host" and not flags:
        return installer
    refresh = installer + " -s -- --no-provision"
    return refresh + " && " + upgrade if step == "update_host" else refresh


def installer_line(repo):
    runner = "sh" if repo == DEFAULT_REPO else "PAIRTASK_REPO=%s sh" % shlex.quote(repo)
    return "curl -fsSL https://raw.githubusercontent.com/%s/main/install.sh | %s" % (repo, runner)


# --- the report --------------------------------------------------------------------

def open_lookup(scope, network):
    if not network:
        return None
    try:
        return Lookup(scope.repo)
    except ValueError:
        return None


def report(scope, refresh=False, network=True, budget=OBSERVE_BUDGET):
    """Schema 1: the observed app and host, the available release, the step.
    Without the network it reads cached metadata only and observes once,
    within budget seconds."""
    cache, lookup = load_cache(scope.repo), open_lookup(scope, network)
    architectures = []
    if lookup is not None:
        architectures = [i["architecture"] for i in Observer(scope.project, scope.remote, budget).app().values()
                         if i["architecture"]]
    available = latest_release(lookup, cache, refresh, architectures)
    # Observed (again) after the slow part, so the report is never older
    # than its release lookup: a rollout meanwhile shows as what now runs.
    observer = Observer(scope.project, scope.remote, budget)
    instances = observer.app()
    host = observer.host()
    app = app_summary(instances, installed_releases(lookup, cache, instances))
    if lookup is not None:
        save_cache(scope.repo, cache)
    update, step = decide(app, host, available)
    app.pop("members")
    return {"schema": 1, "repo": scope.repo, "project": scope.project, "remote": scope.remote, "app": app,
            "host": host, "available": available, "update": update, "guidance": guidance(step, scope),
            "scope": "custom" if scope_flags(scope) else "default"}


def observed_deployment(data):
    """Both containers were seen, so there is a deployment to report on."""
    return set(data["app"]["instances"]) == set(INSTANCES)


# --- presentation ----------------------------------------------------------------

def build_name(tag):
    build = parse_build(tag)
    return "build %d.%d" % build if build else (tag or "unknown")


def age_text(seconds):
    if seconds is None:
        return "never checked"
    for unit, size in (("d", 86400), ("h", 3600), ("m", 60)):
        if seconds >= size:
            return "checked %d%s ago" % (seconds // size, unit)
    return "checked just now"


def summary_line(data):
    app, available = data["app"], data["available"]
    running = build_name(app["release"]) if app["state"] == "known" else "app " + app["state"]
    text = "version: %s; latest %s (%s)" % (running, build_name(available["release"]),
                                            age_text(available["age_seconds"]))
    if data["guidance"]:
        text += "; to update, on this host: " + data["guidance"]
    return text


def with_commit(name, commit, suffix=""):
    return name + (" (commit %s%s)" % (commit[:12], suffix) if commit else "")


def lines(data):
    app, host, available, update = data["app"], data["host"], data["available"], data["update"]
    app_name = build_name(app["release"]) if app["state"] == "known" else app["state"]
    host_name = build_name(host["release"]) if host["state"] == "known" else "unknown"
    error = "; " + available["error"] if available["error"] else ""
    step = data["guidance"] or {"current": "up to date"}.get(update["state"], "no step suggested")
    return ["app:       " + with_commit(app_name, app["commit"]),
            "host:      " + with_commit(host_name, host["commit"], ", modified" if host["dirty"] else ""),
            "available: %s (%s%s)" % (build_name(available["release"]), age_text(available["age_seconds"]), error),
            "update:    " + step]


# --- the chat's notice ----------------------------------------------------------

def notice_argv(data):
    """`pairtask operator update-notice set` in secured, from validated fields only."""
    app, host, available, update = data["app"], data["host"], data["available"], data["update"]
    fields = [("--state", update["state"]), ("--action", update["action"]),
              ("--app-release", app["release"]), ("--app-commit", app["commit"]),
              ("--host-release", host["release"]), ("--available-release", available["release"]),
              ("--available-state", available["state"]), ("--scope", data["scope"]),
              ("--checked-at", available["checked_at"]), ("--observed-at", app["observed_at"])]
    argv = ["incus", "--project", data["project"], "exec", data["remote"] + ":secured", "-T", "--",
            "pairtask", "operator", "update-notice", "set"]
    for flag, value in fields:
        if value:
            argv += [flag, value]
    return argv + ["--json", "--socket", OPERATOR_SOCKET]


def publish(data, timeout=INCUS_TIMEOUT):
    """Hand the result to the broker for the chat; an older broker refuses,
    which is reported and otherwise ignored."""
    try:
        result = subprocess.run(notice_argv(data), capture_output=True, text=True, timeout=timeout, check=False)
    except (OSError, subprocess.TimeoutExpired):
        return False
    return result.returncode == 0


def announce(project, remote, budget=CHAT_BUDGET):
    """Before the chat starts: a notice from a fresh observation and cached
    metadata only, all within budget seconds, then a detached refresh that
    asks GitHub and publishes again. The chat never waits on the network;
    without a deployment to observe nothing is sent or started."""
    started = now()
    data = best_effort(lambda: report(Scope(project, remote, DEFAULT_REPO), network=False, budget=budget * 0.6))
    if data is None or not observed_deployment(data):
        return False
    publish(data, timeout=max(0.5, budget - (now() - started)))
    refresh_in_background(project, remote)
    return True


def best_effort(produce):
    """The value, or None when producing it failed in any way: version
    reporting is information, and must never stop a chat, a status or an
    upgrade that otherwise worked."""
    try:
        return produce()
    except Exception:  # noqa: BLE001 -- informational only; the caller goes on without it
        return None


def refresh_in_background(project, remote):
    script = Path(__file__).resolve().parent / "sandbox-version.py"
    try:
        subprocess.Popen([sys.executable, str(script), "--refresh", "--quiet", "--project", project, "--remote", remote],
                         stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
                         start_new_session=True, close_fds=True)
    except OSError:
        pass
