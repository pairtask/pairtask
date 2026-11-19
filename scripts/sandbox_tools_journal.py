"""The journal of a client update (pairtask update): the one record of where
the transaction stands, the lock every update command holds, and the checks
a record must pass before anything acts on it.

Recovery reads what the containers report and compares it with this record,
so a record whose shape, or whose fields for its phase, would have to be
guessed is refused, and left exactly as it is, before any container is
touched. The transaction itself is sandbox_tools_update.Update.
"""

import fcntl
import json
import os
from pathlib import Path
import time

from sandbox_operator_state import private_write

SCHEMA = 1
# Workspace flips first, then secured.
ROLES = ("workspace", "secured")
PHASES = ("resolving", "staging", "closing", "reopening", "flipping", "verifying", "releasing", "restoring",
          "aborting", "done", "aborted", "failed", "superseded")
TERMINAL = ("done", "aborted", "failed", "superseded")
# Admission is shut, or may be, in these phases.
COMMITTING = ("closing", "reopening", "flipping", "verifying", "releasing", "restoring")
# Failures the next update command retries by itself, and those where
# nothing moves until the operator acts.
AUTO_RECOVERABLE = ("release", "restore", "reopen")
EXPLICIT_ONLY = ("deployment-changed", "unexpected-pointer", "foreign-state", "container-unreachable", "abort")
UNREACHABLE = "container-unreachable"
# Phases that can only be reached with a candidate planned for each
# container, and those that also have a target.
PLANNED = ("staging",) + COMMITTING + ("done",)
DIRECTED = COMMITTING + ("done",)
# The phase each retried failure is retried in.
RETRIED_IN = {"release": "releasing", "restore": "restoring", "reopen": "reopening", "abort": "aborting"}


class UpdateRefused(ValueError):
    """Nothing was started or changed; the message says why."""


class JournalError(UpdateRefused):
    """The journal can't be read as one; it is kept as it is."""


class Journal:
    """The transaction record and the lock every update command holds."""

    def __init__(self, path, lock_path=None):
        self.path = Path(path)
        self.lock_path = Path(lock_path) if lock_path else self.path.with_suffix(".lock")
        self._descriptor, self._depth = None, 0

    def __enter__(self):
        if self._depth == 0:
            self._descriptor = self._acquire()
        self._depth += 1
        return self

    def __exit__(self, *_):
        self._depth -= 1
        if self._depth == 0:
            os.close(self._descriptor)
            self._descriptor = None

    def _acquire(self):
        descriptor = os.open(self.lock_path, os.O_CREAT | os.O_RDWR | os.O_NOFOLLOW, 0o600)
        try:
            fcntl.flock(descriptor, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError as error:
            os.close(descriptor)
            raise UpdateRefused("an update is in progress (phase {})".format(self.phase_unlocked())) from error
        return descriptor

    def phase_unlocked(self):
        try:
            return json.loads(self.path.read_text()).get("phase", "unknown")
        except (OSError, ValueError, AttributeError):
            return "unknown"

    def load(self):
        """The record, None when there has been no update, or JournalError."""
        try:
            text = self.path.read_text()
        except FileNotFoundError:
            return None
        try:
            record = json.loads(text)
        except ValueError as error:
            raise JournalError("the update journal {} isn't valid JSON; it was left as it is".format(self.path)) from error
        problem = malformed(record)
        if problem:
            raise JournalError("the update journal {} is malformed ({}); it was left as it is".format(self.path, problem))
        return record

    def write(self, record):
        record["updated"] = time.time()
        private_write(self.path, (json.dumps(record, indent=2, sort_keys=True) + "\n").encode())


def malformed(record):
    """Why record isn't a journal this code can act on, or None. Checked
    before any container is touched: recovery never works from a record
    whose shape, or whose fields for its phase, it would have to guess."""
    if not isinstance(record, dict) or record.get("schema") != SCHEMA:
        return "unknown schema"
    return first_problem(record, (bad_identity, bad_progress, bad_deployment, bad_containers))


def text(value):
    return isinstance(value, str) and value != ""


def bad_identity(record):
    if not text(record.get("id")):
        return "no update ID"
    if not known_kind(record):
        return "unknown kind or request"
    if not return_generations(record.get("rollback")):
        return "the generations to return to aren't one for each container"
    return None


def known_kind(record):
    if record.get("kind") not in ("update", "rollback"):
        return False
    return isinstance(record.get("requested"), dict)


def return_generations(back):
    """Empty, or one named generation for each container."""
    if not isinstance(back, dict):
        return False
    if back and set(back) != set(ROLES):
        return False
    return all(text(generation) for generation in back.values())


def bad_progress(record):
    """Phase, step, target, outcome and failure agree with each other."""
    if not known_step(record):
        return "unknown phase or step"
    if not count(record.get("restores")):
        return "restores isn't a count"
    if record["phase"] == "superseded":  # inert: it keeps what it was when the root was replaced
        return None
    return first_problem(record, (bad_failure, bad_target, bad_outcome))


def first_problem(record, checks):
    for check in checks:
        problem = check(record)
        if problem:
            return problem
    return None


def known_step(record):
    if record.get("phase") not in PHASES:
        return False
    return record.get("step") in ("intent", "result")


def count(value):
    if isinstance(value, bool) or not isinstance(value, int):
        return False
    return value >= 0


def bad_failure(record):
    """A reason exactly when the record failed, and a known one."""
    if record["phase"] != "failed":
        return None if record.get("failure") is None else "a failure reason without a failed phase"
    if record.get("failure") not in AUTO_RECOVERABLE + EXPLICIT_ONLY:
        return "failed without a known reason"
    return None


def bad_target(record):
    """The target a phase needs: any once pointers may move, the candidate
    while both containers are only being closed, the predecessor on the
    way back."""
    target, chosen = record.get("target"), ("candidate", "predecessor")
    allowed = chosen if committed(record) else chosen + (None,)
    if target not in allowed:
        return "no target for phase {}".format(record["phase"])
    acting = acting_phase(record)
    if acting in ("closing", "reopening") and target != "candidate":
        return "closing towards the predecessor"
    if acting == "restoring" and target != "predecessor":
        return "restoring without the predecessor as target"
    return None


def acting_phase(record):
    """The phase a record stands for: its own, or for a failure that
    recovery retries, the phase it will be retried in."""
    if record["phase"] != "failed":
        return record["phase"]
    return RETRIED_IN.get(record.get("failure"))


def bad_outcome(record):
    """An outcome exactly when the record is done, and then its target."""
    if record["phase"] != "done":
        return None if record.get("outcome") is None else "an outcome before the update is done"
    return None if record.get("outcome") == record.get("target") else "done with an outcome that isn't its target"


def committed(record):
    """The record is in, or failed out of, the phases that move pointers:
    it must name a target, and a candidate for each container. A failure
    that recovery retries by itself came from there."""
    if record.get("phase") == "failed":
        return record.get("failure") in AUTO_RECOVERABLE
    return record.get("phase") in DIRECTED


def bad_deployment(record):
    instances = instances_of(record.get("deployment"))
    if instances is None:
        return "no deployment identity"
    if not all(named_instance(instances[role]) for role in ROLES):
        return "an instance has no name or UUID"
    return None


def instances_of(deployment):
    """The deployment's instance table when it has one for each role."""
    if not isinstance(deployment, dict):
        return None
    instances = deployment.get("instances")
    if not isinstance(instances, dict) or set(instances) != set(ROLES):
        return None
    return instances


def named_instance(instance):
    if not isinstance(instance, dict):
        return False
    return text(instance.get("name")) and text(instance.get("uuid"))


def bad_containers(record):
    containers = record.get("containers")
    if not isinstance(containers, dict) or set(containers) != set(ROLES):
        return "containers aren't workspace and secured"
    planned = record["phase"] in PLANNED or committed(record)
    problems = (bad_entry(role, containers[role], planned) for role in ROLES)
    return next((problem for problem in problems if problem), None)


def bad_entry(role, entry, planned):
    if not isinstance(entry, dict) or not text(entry.get("predecessor")):
        return "{} has no predecessor".format(role)
    if not candidate_named(entry.get("candidate"), planned):
        return "{} has no candidate".format(role)
    return None


def candidate_named(candidate, planned):
    """A named candidate, or none yet while nothing is planned."""
    if candidate is None:
        return not planned
    return text(candidate)


JOURNAL_NAME = "tools-update.json"
LOCK_NAME = "tools-update.lock"


def journal_in(directory):
    """The update journal of the deployment whose operator state is in
    directory."""
    return Journal(Path(directory) / JOURNAL_NAME, Path(directory) / LOCK_NAME)
