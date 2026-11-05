"""A command-recording simulated VM; never starts a subprocess."""
import contextlib
import copy
import itertools
import json
import subprocess
from unittest.mock import patch

from incus_upgrade_fixtures import upgrade, pins_for, transaction_for

BACKUP_SERIAL = itertools.count(1)


class RecordingRunner:
    """A simulated VM command boundary; it never invokes subprocess.run."""

    def __init__(self, obs, fail=None, interrupt=None):
        self.calls = []
        self.holds = set(obs.holds)
        self.units = copy.deepcopy(obs.units)
        self.fail = fail
        self.interrupt = interrupt
        self.server = "7.5"
        self.policy = None
        self.created_policy = None
        self.archive = "var/lib/incus/\nvar/lib/incus/database/\netc/subuid\netc/subgid\n"
        self.probe_state = None
        self.exception = None
        self.after_start_probe = None
        self.started = False
        self.hold_noop = False
        self.query_effect = None
        self.backup_override = None

    def vm(self, argv, timeout):
        argv = list(argv)
        self.calls.append(("vm", argv, timeout))
        raise_injected(self, argv)
        refused = fake_refusal(self, argv)
        if refused is not None:
            return refused
        return subprocess.CompletedProcess(argv, 0, fake_reply(self, argv), "")

    def incus(self, argv, timeout):
        self.calls.append(("incus", list(argv), timeout))
        if self.query_effect:
            self.query_effect()
        answer = {"environment": {"server_version": self.server}}
        return subprocess.CompletedProcess(argv, 0, json.dumps(answer), "")


def creates_policy(argv):
    return argv[:2] == ["sh", "-c"] and "printf '" in argv[2] and upgrade.POLICY in argv[2]


def written_policy(argv):
    return argv[2].split("printf '", 1)[1].split("' >", 1)[0]


def raise_injected(runner, argv):
    if runner.exception and runner.exception[0](argv):
        error, runner.exception = runner.exception[1], None
        raise error
    if runner.interrupt and runner.interrupt(argv):
        runner.interrupt = None
        raise KeyboardInterrupt()


def fake_refusal(runner, argv):
    if runner.fail and runner.fail(argv):
        runner.fail = None
        return subprocess.CompletedProcess(argv, 1, "", "injected failure")
    if creates_policy(argv) and runner.policy is not None:
        return subprocess.CompletedProcess(argv, 1, "", "file already exists")
    if argv == ["test", "-e", upgrade.POLICY]:
        return subprocess.CompletedProcess(argv, 0 if runner.policy is not None else 1, "", "")
    return None


def fake_reply(runner, argv):
    if creates_policy(argv):
        runner.policy = runner.created_policy = written_policy(argv)
        return ""
    handlers = {"apt-mark": fake_holds, "systemctl": fake_systemctl, "mktemp": fake_backup_directory,
                "tar": fake_tar, "cat": fake_cat, "rm": fake_remove}
    handler = handlers.get(argv[0])
    return handler(runner, argv) if handler else ""


def fake_holds(runner, argv):
    if argv[1] == "showhold":
        return "\n".join(sorted(runner.holds)) + "\n"
    if argv[1] == "hold" and runner.hold_noop:
        return ""
    actions = {"unhold": runner.holds.difference_update, "hold": runner.holds.update}
    action = actions.get(argv[1])
    if action:
        action(argv[2:])
    return ""


def fake_backup_directory(runner, argv):
    return (runner.backup_override or f"{upgrade.BACKUP_DIR}/pairtask-incus-{next(BACKUP_SERIAL):010d}") + "\n"


def fake_tar(runner, argv):
    return runner.archive if argv[:2] == ["tar", "-tzf"] else ""


def fake_cat(runner, argv):
    return (runner.policy or "") if argv == ["cat", upgrade.POLICY] else ""


def fake_remove(runner, argv):
    if argv == ["rm", "-f", upgrade.POLICY]:
        runner.policy = None
    return ""


def fake_systemctl(runner, argv):
    if argv[1] == "is-active":
        restored = runner.after_start_probe if runner.started else None
        return (restored or runner.probe_state or runner.units[argv[-1]].active) + "\n"
    if argv[1] == "is-enabled":
        return runner.units[argv[-1]].enabled + "\n"
    changes = {"start": ("active", "active"), "stop": ("active", "inactive"),
               "enable": ("enabled", "enabled"), "disable": ("enabled", "disabled")}
    for name in argv[2:]:
        fake_unit_change(runner, name, argv[1], changes.get(argv[1]))
    return ""


def fake_unit_change(runner, name, action, change):
    unit = runner.units.get(name)
    if unit is not None and change is not None:
        setattr(unit, *change)
        runner.started = runner.started or action == "start"


def installs(argv):
    return argv[:2] == ["apt-get", "install"]


def stops(argv):
    return argv[:2] == ["systemctl", "stop"]


@contextlib.contextmanager
def recorded_apply(obs, fresh=None, fail=None, interrupt=None):
    runner = RecordingRunner(obs, fail, interrupt)
    current = fresh or copy.deepcopy(obs)
    pins = pins_for(current)
    with patch.object(upgrade, "observe", return_value=current), \
            patch.object(upgrade, "trusted_pins", return_value=(pins, transaction_for(pins))) as trusted:
        yield runner, trusted
