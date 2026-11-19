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
import subprocess
import time
import uuid

from sandbox_tools_journal import (  # noqa: F401  (the journal's names are this module's API too)
    SCHEMA, ROLES, PHASES, TERMINAL, COMMITTING, AUTO_RECOVERABLE,
    EXPLICIT_ONLY, UNREACHABLE, PLANNED, DIRECTED, RETRIED_IN, UpdateRefused,
    JournalError, Journal, malformed, JOURNAL_NAME, LOCK_NAME, journal_in)

# How often one transaction may turn back before it is left for the operator.
MAX_RESTORES = 2


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
    touches no container. of(deployment) adds what else identifies the
    deployment (project, release and image lock digests)."""

    def __init__(self, journal, containers, resolve=None, hooks=None):
        self.journal, self.containers, self.resolve = journal, containers, resolve
        self.hooks = hooks or Hooks()
        self.deployment = {}
        self.plan = None

    def of(self, deployment):
        """This transaction, for the deployment these facts identify."""
        self.deployment = dict(deployment or {})
        return self

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
        if previous is None:
            return None
        if previous["phase"] not in TERMINAL:
            raise UpdateRefused("the last update stopped in phase {}".format(previous["phase"]))
        if previous["phase"] == "failed" and not repair:
            raise UpdateRefused("the last update failed ({}); run pairtask update --rollback, or pairtask upgrade"
                                .format(previous.get("failure")))
        return previous

    def open(self, previous, kind, requested):
        """A new record in phase resolving, once both containers answer and
        are released. Nothing has changed if this refuses."""
        update_id = str(uuid.uuid4())
        observed = {role: self.preflight(role, update_id) for role in ROLES}
        try:
            instances = self.instances()
        except ContainerError as error:
            raise UpdateRefused("deployment identity unavailable: {}; nothing was changed".format(error)) from error
        record = {
            "schema": SCHEMA, "id": update_id, "kind": kind, "created": time.time(),
            "deployment": dict(self.deployment, instances=instances),
            "requested": requested, "override": None,
            "phase": "resolving", "step": "intent", "target": None, "outcome": None, "failure": None, "detail": None,
            "restores": 0, "rollback": return_to(previous) or {},
            "containers": {role: {"candidate": None, "predecessor": observed[role]["current"], "fresh": None,
                                  "observed": observed[role]} for role in ROLES},
        }
        self.intent(record, "resolving")
        return record

    def preflight(self, role, update_id):
        """What a container reports before anything starts; refuses unless
        it answers, is released, and runs a client set that verifies."""
        name = self.containers[role].name
        try:
            seen = self.containers[role].activate("verify", "--update", update_id)
        except ContainerError as error:
            raise UpdateRefused("{}: {}; nothing was changed".format(name, error)) from error
        problem = unsound(name, seen)
        if problem:
            raise UpdateRefused(problem)
        return seen

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
        identities = {}
        for role in ROLES:
            instance = self.containers[role]
            identity = {"name": instance.name, "uuid": instance.uuid}
            provenance = getattr(instance, "provenance", None)
            if provenance is not None:
                identity.update(provenance())
            identities[role] = identity
        return identities

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
        record["containers"][role]["observed"] = seen
        classify(record, role, seen)
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
            self.release_closed(record, seen)
        except ContainerError as error:
            return self.fail(record, "release", error)
        seen = {role: self.observe(record, role) for role in ROLES}
        if not all(released(record, role, seen[role]) for role in ROLES):
            return self.fail(record, "release")
        self.result(record)
        return self.finish(record, "done", outcome=record["target"])

    def release_closed(self, record, seen):
        """Reopen each container that is still closed for this update."""
        for role in ROLES:
            if seen[role]["state"] == "maintenance":
                self.call(record, role, "release")

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
            phase = RETRIED_IN[record["failure"]]
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


def unsound(name, seen):
    """Why an update can't start from what a container reports, or None."""
    if seen.get("state") != "ready" or not seen.get("current"):
        return "{} isn't released and ready; nothing was changed".format(name)
    if seen.get("entries_ok") is not True:
        return ("{}: the client set it runs doesn't verify, so there would be nothing sound to return to; "
                "nothing was changed (pairtask upgrade replaces it)".format(name))
    return None


def classify(record, role, seen):
    """A container's pointer is one of the two the journal names, and its
    state is this update's or an open one; else Mismatch."""
    entry = record["containers"][role]
    expected = {entry["predecessor"], entry["candidate"]} - {None}
    if seen.get("current") not in expected:
        raise Mismatch("unexpected-pointer", "{} runs generation {}, which this update doesn't know".format(role, seen.get("current")))
    if seen.get("state") != "ready" and not closed_for(record, seen):
        raise Mismatch("foreign-state", "{} is in state {} for another update".format(role, seen.get("state")))


def closed_for(record, seen):
    return seen.get("state") == "maintenance" and seen.get("update") == record["id"]


def released(record, role, seen):
    """Open again, on the generation the target names."""
    if seen["state"] != "ready":
        return False
    return verified(record, role, seen, or_ready=True)


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


@contextmanager
def admission(directory):
    """Check admission while excluding updates; hold through launching,
    never through the lifetime of the admitted session."""
    if directory is None:
        yield
        return
    with journal_in(directory) as journal:
        reason = session_refusal(journal.load())
        if reason:
            raise UpdateRefused(reason)
        yield


def require_finished(journal):
    """An upgrade cannot discard an interrupted transaction's evidence."""
    record = journal.load()
    if record is not None and record["phase"] not in TERMINAL:
        raise UpdateRefused("a client update is unfinished (phase {}); run pairtask update --repair before upgrading"
                            .format(record["phase"]))


def glance(directory):
    """The last record, read without the lock, for commands that only need
    to know where an update stands: the journal is replaced atomically, so
    a read sees one whole record. None when there is none or it can't be
    read; those commands never fail on it."""
    try:
        return journal_in(directory).load()
    except (UpdateRefused, OSError):
        return None


def in_effect(record):
    """An update that took effect and named the versions it moved to."""
    if record is None or record["phase"] != "done":
        return False
    return record.get("outcome") == "candidate" and bool(record.get("requested"))


def clients_line(record):
    """What `pairtask version` says about an update in effect, or None."""
    if not in_effect(record):
        return None
    versions = ", ".join("{} {}".format(name, record["requested"][name]) for name in sorted(record["requested"]))
    return "clients:   {} (pairtask update; the next upgrade returns to the release's own)".format(versions)


def supersede(journal):
    """End a finished update after root replacement. An unfinished record
    retains its recovery evidence and must be repaired before upgrading."""
    with journal:
        record = journal.load()
        if record is None or record["phase"] == "superseded":
            return record
        if record["phase"] not in TERMINAL:
            raise UpdateRefused("a client update is unfinished (phase {}); finish it with pairtask update --repair first"
                                .format(record["phase"]))
        record.update(phase="superseded", step="result")
        journal.write(record)
        return record
