"""pairtask update, host side: one journaled transaction that moves the agent
clients of both containers to a new generation together, or leaves both on
the one they had.

Each container does its half through `pairtask-tools-activate` (see
infra/image/tool_update.py); this module decides the order and survives
dying at any point. The rules it keeps:

- Every step is written to the journal as an intent before the containers
  are touched and as a result after, so a crash is always an intent whose
  outcome is read back from the containers, never assumed.
- Admission is shut in both containers before either pointer moves, and
  reopened only after both verify against the journal's target.
- `target` (candidate or predecessor) picks the destination. Going back is
  written durably before the first restore.
- What a container reports is classified against the journal. A pointer or
  a state the journal doesn't expect stops everything; nothing is guessed.
- Nothing is pruned.

The journal lives beside the operator state, with a separate lock file that
is created once and never replaced.
"""

from contextlib import contextmanager
import fcntl
import json
import os
from pathlib import Path
import subprocess
import time
import uuid

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
# How often one transaction may turn back before it is left for the operator.
MAX_RESTORES = 2


class UpdateRefused(ValueError):
    """Nothing was started or changed; the message says why."""


class JournalError(UpdateRefused):
    """The journal can't be read as one; it is kept as it is."""


class ContainerError(Exception):
    """An activator step that failed (code "refused") or a container that
    couldn't be reached (code UNREACHABLE). A failure says nothing about
    whether the step took effect."""

    def __init__(self, code, message):
        super().__init__(message)
        self.code = code


class Blocked(Exception):
    """A container can't be reached or read; the journal stays where it is
    and the next command resumes."""

    def __init__(self, record, reason):
        super().__init__("blocked({}): {}".format(UNREACHABLE, reason))
        self.record = record


class Crash(BaseException):
    """For tests: raised from a hook to stop the host at an exact boundary."""


class Mismatch(Exception):
    """A container reports something the journal doesn't expect."""

    def __init__(self, failure, detail):
        super().__init__(detail)
        self.failure = failure


class Hooks:
    """Called at every boundary; all do nothing by default. step is "intent"
    or "result" for a journal write (container None), or the activator
    subcommand for a container action. A test raises Crash from one. fail
    runs just before an activator call and replied just after it, so a fake
    can refuse a call, or lose the reply of one that took effect, by raising
    ContainerError."""

    def before(self, phase, step, container):
        pass

    def after(self, phase, step, container):
        pass

    def fail(self, container, subcommand):
        pass

    def replied(self, container, subcommand):
        pass


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
    for check in (bad_identity, bad_progress, bad_deployment, bad_containers):
        problem = check(record)
        if problem:
            return problem
    return None


def text(value):
    return isinstance(value, str) and value != ""


def bad_identity(record):
    if not text(record.get("id")):
        return "no update ID"
    if record.get("kind") not in ("update", "rollback") or not isinstance(record.get("requested"), dict):
        return "unknown kind or request"
    back = record.get("rollback")
    if not isinstance(back, dict) or (back and set(back) != set(ROLES)) or not all(text(generation) for generation in back.values()):
        return "the generations to return to aren't one for each container"
    return None


def bad_progress(record):
    """Phase, step, target, outcome and failure agree with each other."""
    phase = record.get("phase")
    if phase not in PHASES or record.get("step") not in ("intent", "result"):
        return "unknown phase or step"
    restores = record.get("restores")
    if not isinstance(restores, int) or isinstance(restores, bool) or restores < 0:
        return "restores isn't a count"
    if phase == "superseded":  # inert: it keeps what it was when the root was replaced
        return None
    return bad_failure(record) or bad_target(record) or bad_outcome(record)


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
    if target not in chosen + (None,) or (committed(record) and target not in chosen):
        return "no target for phase {}".format(record["phase"])
    if record["phase"] in ("closing", "reopening") and target != "candidate":
        return "closing towards the predecessor"
    if heading_back(record) and target != "predecessor":
        return "restoring without the predecessor as target"
    return None


def heading_back(record):
    return record["phase"] == "restoring" or (record["phase"] == "failed" and record.get("failure") == "restore")


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
    deployment = record.get("deployment")
    instances = deployment.get("instances") if isinstance(deployment, dict) else None
    if not isinstance(instances, dict) or set(instances) != set(ROLES):
        return "no deployment identity"
    if not all(isinstance(instances[role], dict) and text(instances[role].get("name")) and text(instances[role].get("uuid"))
               for role in ROLES):
        return "an instance has no name or UUID"
    return None


def bad_containers(record):
    containers = record.get("containers")
    if not isinstance(containers, dict) or set(containers) != set(ROLES):
        return "containers aren't workspace and secured"
    planned = record["phase"] in PLANNED or committed(record)
    for role in ROLES:
        entry = containers[role]
        if not isinstance(entry, dict) or not text(entry.get("predecessor")):
            return "{} has no predecessor".format(role)
        candidate = entry.get("candidate")
        if not (text(candidate) or (candidate is None and not planned)):
            return "{} has no candidate".format(role)
    return None


@contextmanager
def reported(record):
    """Container failures inside become ContainerError; one that can't be
    reached becomes Blocked, with the journal untouched."""
    try:
        yield
    except ContainerError as error:
        if error.code == UNREACHABLE:
            raise Blocked(record, error) from error
        raise


class Update:
    """The transaction over both containers.

    containers maps "workspace" and "secured" to objects with `name`, `uuid`
    and `activate(subcommand, *args) -> dict` (ContainerError on failure).
    resolve(requested, record) returns {"requested": {client: version},
    "override": {...} or None, "containers": {role: {"candidate": ID,
    "deliver": callable(container, update_id) -> the stage arguments}}}; it
    touches no container. deployment is what else identifies the deployment
    (project, release and image lock digests)."""

    def __init__(self, journal, containers, resolve=None, hooks=None, deployment=None):
        self.journal, self.containers, self.resolve = journal, containers, resolve
        self.hooks = hooks or Hooks()
        self.deployment = dict(deployment or {})
        self.plan = None

    # Commands

    def run(self, requested):
        """Update the clients to the requested versions (latest for those
        left out). Returns the terminal record."""
        with self.journal:
            previous = self.settled()
            record = self.open(previous, "update", dict(requested))
            try:
                self.plan = self.resolve(dict(requested), record)
            except (ValueError, KeyError, OSError) as error:
                return self.finish(record, "aborted", detail="resolving failed: {}".format(error))
            return self.planned(record)

    def rollback(self):
        """Return both containers to the client set they ran before the last
        completed update, as a new transaction and without any download."""
        with self.journal:
            previous = self.settled(repair=True)
            back = return_to(previous)
            if not back:
                raise UpdateRefused("there is no earlier client set to return to")
            record = self.open(previous, "rollback", {})
            self.plan = {"requested": {}, "override": None, "containers": {
                role: {"candidate": back[role], "deliver": reuse(back[role])} for role in ROLES}}
            return self.planned(record)

    def recover(self, repair=False):
        """Finish or revert an interrupted update. Returns the record (None
        when there has been no update); raises Blocked when a container
        can't be reached. With repair, a failed abort is retried too."""
        with self.journal:
            record = self.journal.load()
            if record is None or not resumable(record, repair):
                return record
            try:
                self.same_deployment(record)
                observed = {role: self.observe(record, role) for role in ROLES}
                phase, changes = self.resume(record, observed)
            except Mismatch as error:
                return self.finish(record, "failed", failure=error.failure, detail=str(error))
            self.intent(record, phase, **changes)
            return self.drive(record)

    def status(self):
        """The journal and what it means for starting sessions."""
        with self.journal:
            record = self.journal.load()
        return {"journal": record, "refusal": session_refusal(record), "return_to": return_to(record)}

    # Opening a transaction

    def settled(self, repair=False):
        """The last record after recovery; refuses while it isn't over."""
        previous = self.recover(repair)
        if previous is not None and previous["phase"] not in TERMINAL:
            raise UpdateRefused("the last update stopped in phase {}".format(previous["phase"]))
        if previous is not None and previous["phase"] == "failed" and not repair:
            raise UpdateRefused("the last update failed ({}); run pairtask update --rollback, or pairtask upgrade"
                                .format(previous.get("failure")))
        return previous

    def open(self, previous, kind, requested):
        """A new record in phase resolving, once both containers answer and
        are released. Nothing has changed if this refuses."""
        update_id = str(uuid.uuid4())
        observed = {}
        for role in ROLES:
            try:
                observed[role] = self.containers[role].activate("verify", "--update", update_id)
            except ContainerError as error:
                raise UpdateRefused("{}: {}; nothing was changed".format(self.containers[role].name, error)) from error
            if observed[role].get("state") != "ready" or not observed[role].get("current"):
                raise UpdateRefused("{} isn't released and ready; nothing was changed".format(self.containers[role].name))
            if observed[role].get("entries_ok") is not True:
                raise UpdateRefused("{}: the client set it runs doesn't verify, so there would be nothing sound to return to; "
                                    "nothing was changed (pairtask upgrade replaces it)".format(self.containers[role].name))
        record = {
            "schema": SCHEMA, "id": update_id, "kind": kind, "created": time.time(),
            "deployment": dict(self.deployment, instances=self.instances()),
            "requested": requested, "override": None,
            "phase": "resolving", "step": "intent", "target": None, "outcome": None, "failure": None, "detail": None,
            "restores": 0, "rollback": return_to(previous) or {},
            "containers": {role: {"candidate": None, "predecessor": observed[role]["current"], "fresh": None,
                                  "observed": observed[role]} for role in ROLES},
        }
        self.intent(record, "resolving")
        return record

    def planned(self, record):
        """Record the plan and run it; a plan that changes nothing ends here."""
        record["requested"], record["override"] = self.plan["requested"], self.plan.get("override")
        for role in ROLES:
            record["containers"][role]["candidate"] = self.plan["containers"][role]["candidate"]
        if all(record["containers"][role]["candidate"] == record["containers"][role]["predecessor"] for role in ROLES):
            return self.finish(record, "aborted", detail="both containers already run these clients")
        self.result(record)
        self.intent(record, "staging")
        return self.drive(record)

    def instances(self):
        return {role: {"name": self.containers[role].name, "uuid": self.containers[role].uuid} for role in ROLES}

    def same_deployment(self, record):
        """The containers are the ones the journal was written for."""
        try:
            now = dict(self.deployment, instances=self.instances())
        except ContainerError as error:
            raise Blocked(record, error) from error
        if now != record.get("deployment"):
            raise Mismatch("deployment-changed", "the containers or the release changed since this update began")

    # The journal's write order

    def intent(self, record, phase, **changes):
        self.hooks.before(phase, "intent", None)
        record.update(changes, phase=phase, step="intent")
        self.journal.write(record)
        self.hooks.after(phase, "intent", None)

    def result(self, record):
        phase = record["phase"]
        self.hooks.before(phase, "result", None)
        record["step"] = "result"
        self.journal.write(record)
        self.hooks.after(phase, "result", None)

    def finish(self, record, phase, **changes):
        record.update(changes, phase=phase, step="result")
        self.journal.write(record)
        return record

    def fail(self, record, failure, error=None):
        return self.finish(record, "failed", failure=failure, detail=str(error) if error else record.get("detail"))

    def call(self, record, role, subcommand, *args):
        phase = record["phase"]
        self.hooks.before(phase, subcommand, role)
        with reported(record):
            self.hooks.fail(role, subcommand)
            reply = self.containers[role].activate(subcommand, *args, "--update", record["id"])
            self.hooks.replied(role, subcommand)
        self.hooks.after(phase, subcommand, role)
        return reply

    def observe(self, record, role):
        """What the container reports, classified: its pointer is one of the
        two the journal names, and its state is ours or an open one."""
        try:
            seen = self.containers[role].activate("verify", "--update", record["id"])
        except ContainerError as error:
            raise Blocked(record, error) from error
        entry = record["containers"][role]
        entry["observed"] = seen
        expected = {entry["predecessor"], entry["candidate"]} - {None}
        if seen.get("current") not in expected:
            raise Mismatch("unexpected-pointer", "{} runs generation {}, which this update doesn't know".format(role, seen.get("current")))
        ours = seen.get("state") == "maintenance" and seen.get("update") == record["id"]
        if not ours and seen.get("state") != "ready":
            raise Mismatch("foreign-state", "{} is in state {} for another update".format(role, seen.get("state")))
        return seen

    # Phases. Each starts with the journal at (phase, intent) and ends by
    # writing the next phase's intent or a terminal record.

    def drive(self, record):
        try:
            while record["phase"] not in TERMINAL:
                getattr(self, "phase_" + record["phase"])(record)
        except Mismatch as error:
            return self.finish(record, "failed", failure=error.failure, detail=str(error))
        return record

    def phase_staging(self, record):
        try:
            for role in ROLES:
                self.stage(record, role)
        except (ContainerError, OSError, ValueError, subprocess.SubprocessError) as error:
            return self.intent(record, "aborting", detail="staging failed: {}".format(error))
        self.result(record)
        return self.intent(record, "closing", target="candidate")

    def stage(self, record, role):
        entry = record["containers"][role]
        with reported(record):
            arguments = self.plan["containers"][role]["deliver"](self.containers[role], record["id"])
        reply = self.call(record, role, "stage", *arguments)
        if reply.get("generation") != entry["candidate"]:
            raise ContainerError("refused", "{} staged another generation than planned".format(role))
        entry["fresh"] = reply.get("fresh")
        self.call(record, role, "smoke", entry["candidate"])

    def phase_closing(self, record):
        try:
            for role in ROLES:
                self.call(record, role, "close")
        except ContainerError as error:
            return self.intent(record, "reopening", detail="closing failed: {}".format(error))
        self.result(record)
        return self.intent(record, "flipping")

    def phase_reopening(self, record):
        """Closing failed before any flip: reopen what closed, as it was."""
        try:
            for role in ROLES:
                seen = self.observe(record, role)
                if seen["current"] != record["containers"][role]["predecessor"]:
                    raise Mismatch("unexpected-pointer", "{} moved before both containers were closed".format(role))
                if seen["state"] == "maintenance":
                    self.call(record, role, "release")
        except ContainerError as error:
            return self.fail(record, "reopen", error)
        self.result(record)
        return self.intent(record, "aborting")

    def phase_flipping(self, record):
        try:
            for role in ROLES:
                self.call(record, role, "flip", record["containers"][role]["candidate"])
        except ContainerError as error:
            return self.turn_back(record, "flipping failed: {}".format(error))
        self.result(record)
        return self.intent(record, "verifying")

    def phase_verifying(self, record):
        seen = {role: self.observe(record, role) for role in ROLES}
        if all(verified(record, role, seen[role]) for role in ROLES):
            self.result(record)
            return self.intent(record, "releasing")
        return self.turn_back(record, "a container didn't verify on its {} generation".format(record["target"]))

    def phase_releasing(self, record):
        """Decide for both first; release only when both verify."""
        seen = {role: self.observe(record, role) for role in ROLES}
        if not all(verified(record, role, seen[role], or_ready=True) for role in ROLES):
            return self.turn_back(record, "a container no longer verifies on its {} generation".format(record["target"]))
        try:
            for role in ROLES:
                if seen[role]["state"] == "maintenance":
                    self.call(record, role, "release")
        except ContainerError as error:
            return self.fail(record, "release", error)
        seen = {role: self.observe(record, role) for role in ROLES}
        if not all(seen[role]["state"] == "ready" and verified(record, role, seen[role], or_ready=True) for role in ROLES):
            return self.fail(record, "release")
        self.result(record)
        return self.finish(record, "done", outcome=record["target"])

    def phase_restoring(self, record):
        """Both closed first, always; then back to each predecessor."""
        if record["restores"] > MAX_RESTORES:
            return self.fail(record, "restore")
        try:
            for role in ROLES:
                self.call(record, role, "close")
            for role in ROLES:
                predecessor = record["containers"][role]["predecessor"]
                if self.observe(record, role)["current"] != predecessor:
                    self.call(record, role, "restore", predecessor)
        except ContainerError as error:
            return self.fail(record, "restore", error)
        self.result(record)
        return self.intent(record, "verifying")

    def phase_aborting(self, record):
        try:
            for role in ROLES:
                self.call(record, role, "abort")
        except ContainerError as error:
            return self.fail(record, "abort", error)
        self.result(record)
        return self.finish(record, "aborted")

    def turn_back(self, record, detail):
        """Head for the predecessors; the target is durable before any
        restore. A transaction already heading back has failed to."""
        if record["target"] == "predecessor" and record["phase"] == "verifying":
            return self.fail(record, "restore")
        return self.intent(record, "restoring", target="predecessor", detail=detail, restores=record["restores"] + 1)

    # Recovery

    def resume(self, record, observed):
        """The phase to re-enter, and the journal changes that go with it."""
        phase = record["phase"]
        if phase == "failed":
            phase = {"release": "releasing", "restore": "restoring", "reopen": "reopening", "abort": "aborting"}[record["failure"]]
            if phase == "restoring":
                return "restoring", {"target": "predecessor", "failure": None, "restores": 0}
            return phase, {"failure": None}
        if phase in ("resolving", "staging", "aborting"):
            return "aborting", {}
        if phase in ("closing", "reopening"):
            return "reopening", {}
        if phase == "releasing":
            return "releasing", {}
        if record["target"] == "predecessor" or phase == "restoring":
            return "restoring", {"target": "predecessor"}
        return self.resume_forward(record, phase, observed)

    def resume_forward(self, record, phase, observed):
        """Flipping or verifying towards the candidates: every container is
        closed before anything else; a flip that didn't reach both turns back."""
        for role in ROLES:
            self.call(record, role, "close")
        arrived = all(observed[role]["current"] == record["containers"][role]["candidate"] and observed[role].get("entries_ok")
                      for role in ROLES)
        if phase == "verifying" or arrived:
            return "verifying", {}
        return "restoring", {"target": "predecessor", "detail": "the flip didn't reach both containers",
                             "restores": record["restores"] + 1}


def resumable(record, repair):
    """A record recovery acts on: one that isn't over, or one that failed in
    a way that is retried (a failed abort only when asked to repair)."""
    if record["phase"] not in TERMINAL:
        return True
    retried = AUTO_RECOVERABLE + (("abort",) if repair else ())
    return record["phase"] == "failed" and record.get("failure") in retried


def verified(record, role, seen, or_ready=False):
    """The container runs the generation the target names, its entries
    resolve, and it is closed for this update (or, when allowed, open)."""
    closed = seen.get("state") == "maintenance" and seen.get("update") == record["id"]
    wanted = record["containers"][role][record["target"]]
    return seen.get("current") == wanted and seen.get("entries_ok") is True and (closed or (or_ready and seen.get("state") == "ready"))


def return_to(record):
    """Per container, the generation `--rollback` would return to after this
    record, or {}: the predecessors of an update that took effect or failed,
    else whatever the record before it would have returned to. Nothing
    after an upgrade: those generations belonged to the root it replaced."""
    if record is None or record["phase"] == "superseded":
        return {}
    took_effect = record["phase"] == "done" and record.get("outcome") == "candidate"
    if took_effect or record["phase"] == "failed":
        return {role: record["containers"][role]["predecessor"] for role in ROLES}
    return dict(record.get("rollback") or {})


def reuse(generation):
    """Stage arguments for a generation the store already holds."""
    return lambda container, update_id: ["--reuse", generation]


def session_refusal(record):
    """Why a host session command must not start a session now, or None."""
    if record is None:
        return None
    if record["phase"] in COMMITTING:
        return "a client update is in progress (phase {}); try again when it has finished".format(record["phase"])
    if record["phase"] == "failed":
        return ("the last client update failed ({}); run pairtask update --rollback, or pairtask upgrade"
                .format(record.get("failure")))
    return None


def supersede(journal):
    """An upgrade replaces the root: a finished journal is marked superseded
    so it is never applied to the new containers; an unfinished one refuses."""
    with journal:
        record = journal.load()
        if record is None or record["phase"] == "superseded":
            return record
        if record["phase"] not in TERMINAL:
            raise UpdateRefused("a client update is unfinished (phase {}); finish it with pairtask update --rollback first"
                                .format(record["phase"]))
        record.update(phase="superseded", step="result")
        journal.write(record)
        return record
