"""Upgrade the held Incus packages of the dedicated Colima VM (#312): a
read-only plan that writes a manifest, and an apply that re-derives and
re-checks everything before it stops Incus, backs it up, installs exactly
the planned versions and restores what it found. Only self-contained `dir`
storage under /var/lib/incus is supported; any other pool stops the plan.

Every command goes through an injected Runner. The manifest is data only:
apply never takes commands, paths or package scope from it.
"""

from dataclasses import asdict, dataclass, field
import json
import os
import posixpath
import re
import secrets
import subprocess
from typing import Dict, List, Optional, Tuple

from sandbox_incus_version import parse_version, server_version, supported

TARGET = {"profile": "pairtask", "remote": "colima-pairtask"}
PACKAGES = ("incus", "incus-base", "incus-client", "incus-extra", "incus-ui-canonical")
UNITS = ("incus.socket", "incus.service")
MANIFEST_SCHEMA = 1
BACKUP_DIR = "/var/backups"
INCUS_DIR = "/var/lib/incus"
POLICY = "/usr/sbin/policy-rc.d"
ORIGIN = "pkgs.zabbly.com/incus/stable"

# Bounds, in seconds.
QUERY, APT, STOP, TAR = 30, 600, 60, 1800
# The backup needs the size of /var/lib/incus plus this much free.
SPACE_MARGIN = 512 * 1024 * 1024
POLICY_BODY = "#!/bin/sh\n# pairtask incus upgrade {nonce}\nexit 101\n"
NAME = re.compile(r"[a-z0-9][a-z0-9+.-]*")
VERSION = re.compile(r"[0-9][0-9A-Za-z.+~:-]*")
# mktemp -d's private, newly made directory for one apply's backup.
BACKUP_RESERVED = re.compile(re.escape(BACKUP_DIR) + r"/pairtask-incus-[A-Za-z0-9]{10}")
CLEANUP = ("remove our policy-rc.d", "restore holds", "restore Incus units")
SNAPSHOT = ("server_version", "holds", "units", "instances", "pools", "packages")

# A simulated transaction line: ("Inst" | "Conf" | "Remv" | ..., package, version).
TxLine = Tuple[str, str, str]


class UpgradeError(ValueError):
    """The upgrade can't go ahead, or a step of it failed; steps are what ran."""

    def __init__(self, message: str, steps: Optional[List["Step"]] = None):
        super().__init__(message)
        self.steps = steps or []


class UpgradeInterrupted(UpgradeError):
    """Apply was interrupted; cleanup ran, and steps say how far it got."""


class Runner:
    """Runs commands for the upgrade; tests inject a fake."""

    def run(self, argv: List[str], timeout: float) -> subprocess.CompletedProcess:
        return subprocess.run(argv, capture_output=True, text=True, check=False, timeout=timeout)

    def vm(self, argv: List[str], timeout: float) -> subprocess.CompletedProcess:
        """A command inside the dedicated VM, as root."""
        return self.run(["colima", "ssh", "-p", TARGET["profile"], "--", "sudo", *argv], timeout)

    def incus(self, argv: List[str], timeout: float) -> subprocess.CompletedProcess:
        """An incus client command against the VM's remote."""
        return self.run(["incus", *argv], timeout)


@dataclass
class Package:
    installed: Optional[str] = None
    candidate: Optional[str] = None
    origin: Optional[str] = None


@dataclass
class Unit:
    active: str = "unknown"   # systemctl is-active: active, inactive, failed, ...
    enabled: str = "unknown"  # systemctl is-enabled: enabled, disabled, static, ...


@dataclass
class Observation:
    server_version: Optional[str] = None
    instances: List[Dict[str, str]] = field(default_factory=list)  # {project, name, status}
    pools: List[Dict[str, str]] = field(default_factory=list)      # {name, driver, source}
    packages: Dict[str, Package] = field(default_factory=dict)
    holds: List[str] = field(default_factory=list)
    units: Dict[str, Unit] = field(default_factory=dict)
    policy_rc_d_exists: bool = False
    # The physical /var/lib/incus (realpath -e), None when unreadable.
    incus_root: Optional[str] = None
    backup_free_bytes: int = 0
    incus_bytes: int = 0
    # Per package, whether its candidate is at least what is installed
    # (dpkg --compare-versions CANDIDATE ge INSTALLED): False is a downgrade,
    # None unknown; both block.
    upgrades: Dict[str, Optional[bool]] = field(default_factory=dict)


@dataclass
class Step:
    name: str
    ok: bool
    detail: str = ""


def observe(runner: Runner) -> Observation:
    """What the VM's Incus and its packages are now; reads only."""
    obs = Observation(server_version=server_version(incus_json(runner, ["query", TARGET["remote"] + ":/1.0"])))
    obs.instances = [{"project": str(i.get("project", "default")), "name": str(i.get("name")), "status": str(i.get("status"))}
                     for i in incus_json(runner, ["list", "--all-projects", "--format", "json", TARGET["remote"] + ":"])]
    obs.pools = [pool_entry(runner, p) for p in incus_json(runner, ["storage", "list", "--format", "json", TARGET["remote"] + ":"])]
    obs.incus_root = realpath(runner, INCUS_DIR)
    obs.packages = parse_policy(vm_out(runner, ["apt-cache", "policy", *PACKAGES], QUERY))
    obs.holds = vm_out(runner, ["apt-mark", "showhold"], QUERY).split()
    obs.units = {unit: unit_state(runner, unit) for unit in UNITS}
    obs.policy_rc_d_exists = runner.vm(["test", "-e", POLICY], QUERY).returncode == 0
    obs.backup_free_bytes = first_int(vm_out(runner, ["df", "--output=avail", "-B1", BACKUP_DIR], QUERY).split()[1:])
    obs.incus_bytes = first_int(vm_out(runner, ["du", "-sb", INCUS_DIR], QUERY).split())
    obs.upgrades = {name: compare(runner, package) for name, package in obs.packages.items()}
    return obs


def pool_entry(runner: Runner, pool: dict) -> Dict[str, Optional[str]]:
    """A pool's name, driver, configured source and physical source."""
    source = str((pool.get("config") or {}).get("source", ""))
    return {"name": str(pool.get("name")), "driver": str(pool.get("driver")), "source": source,
            "resolved": realpath(runner, source) if source.startswith("/") else None}


def realpath(runner: Runner, path: str) -> Optional[str]:
    """The VM's physical path for path, read-only; None when it can't say."""
    result = runner.vm(["realpath", "-e", "--", path], QUERY)
    found = result.stdout.strip()
    return found if result.returncode == 0 and found.startswith("/") else None


def incus_json(runner: Runner, argv: List[str]):
    result = runner.incus(argv, QUERY)
    if result.returncode != 0:
        raise UpgradeError(f"incus {argv[0]} failed: {result.stderr.strip()}")
    try:
        return json.loads(result.stdout)
    except ValueError as err:
        raise UpgradeError(f"incus {argv[0]} answered no JSON") from err


def vm_out(runner: Runner, argv: List[str], timeout: float) -> str:
    result = runner.vm(argv, timeout)
    if result.returncode != 0:
        raise UpgradeError(f"{argv[0]} in the VM failed: {result.stderr.strip()}")
    return result.stdout


def first_int(words: List[str]) -> int:
    try:
        return int(words[0])
    except (IndexError, ValueError):
        return 0


def unit_state(runner: Runner, unit: str) -> Unit:
    active = runner.vm(["systemctl", "is-active", unit], QUERY).stdout.strip() or "unknown"
    enabled = runner.vm(["systemctl", "is-enabled", unit], QUERY).stdout.strip() or "unknown"
    return Unit(active, enabled)


def parse_policy(text: str) -> Dict[str, Package]:
    """apt-cache policy's Installed, Candidate and the candidate's origin."""
    packages = {name: Package() for name in PACKAGES}
    current, version = None, None
    for line in text.splitlines():
        if package_heading(line):
            current, version = packages.get(line.rstrip()[:-1]), None
        elif current is not None:
            version = policy_line(current, line.split(), version)
    return packages


def package_heading(line: str) -> bool:
    """A `name:` line opening one package's apt-cache policy."""
    if not line or line[0].isspace():
        return False
    return line.rstrip().endswith(":")


def policy_line(package: Package, words: List[str], version: Optional[str]) -> Optional[str]:
    """Reads one line into package; the version-table entry it sits under."""
    field = {"Installed:": "installed", "Candidate:": "candidate"}.get(words[0] if words else "")
    if field:
        setattr(package, field, words[1] if len(words) > 1 and words[1] != "(none)" else None)
        return None
    if len(words) >= 2 and words[0] == "***":
        return words[1]
    if len(words) == 2 and not words[0].isdigit():
        return words[0]
    if candidate_source(package, words, version):
        package.origin = package.origin or words[1].split("://", 1)[1].rstrip("/")
    return version


def candidate_source(package: Package, words: List[str], version: Optional[str]) -> bool:
    """A repository line under the candidate's version-table entry."""
    if version is None or version != package.candidate:
        return False
    return len(words) >= 2 and "://" in words[1]


def compare(runner: Runner, package: Package) -> Optional[bool]:
    """Whether the candidate is at least what's installed, as dpkg says."""
    if package.installed is None or package.candidate is None:
        return None
    code = runner.vm(["dpkg", "--compare-versions", package.candidate, "ge", package.installed], QUERY).returncode
    return {0: True, 1: False}.get(code)


def transaction(runner: Runner, pins: Dict[str, str]) -> List[TxLine]:
    """The lines of `apt-get -s install pkg=ver ...` for exactly these pins."""
    return parse_transaction(vm_out(runner, ["apt-get", "-s", "install", "-y", "--no-remove", *pin_args(pins)], APT))


def pin_args(pins: Dict[str, str]) -> List[str]:
    return [f"{name}={version}" for name, version in sorted(pins.items())]


def parse_transaction(text: str) -> List[TxLine]:
    lines = []
    for line in text.splitlines():
        match = re.match(r"(Inst|Conf|Remv|Purg) (\S+)(?: \[([^\]]+)\])?(?: \(([^ )]+))?", line)
        if match:
            op, name, old, new = match.groups()
            lines.append((op, name, new or old or ""))
    return lines


def trusted_pins(runner: Runner, obs: Observation) -> Tuple[Dict[str, str], List[TxLine]]:
    """The pins and transaction derived only from PACKAGES' candidates: all
    five Incus pins, plus every upgraded dependency, re-simulated pinned."""
    base = {name: obs.packages[name].candidate for name in PACKAGES
            if name in obs.packages and obs.packages[name].candidate}
    pins = dict(base)
    for op, name, version in transaction(runner, base):
        if op == "Inst" and name not in pins:
            pins[name] = version
    return pins, transaction(runner, pins)


def blockers(obs: Observation, tx: List[TxLine]) -> List[str]:
    """Every reason this upgrade can't go ahead; empty when it can."""
    found = [f"instance {i.get('project')}/{i.get('name')} is {i.get('status') or 'in an unknown state'}, not Stopped"
             for i in obs.instances if not isinstance(i, dict) or i.get("status") != "Stopped"]
    found += unit_blockers(obs)
    if obs.incus_root != INCUS_DIR:
        found.append(f"{INCUS_DIR} isn't a plain directory here (resolves to {obs.incus_root}); upgrade manually")
    found += [f"storage pool {p['name']} ({p['driver']}) isn't self-contained under {INCUS_DIR}; back it up "
              "and upgrade manually" for p in obs.pools if not self_contained(p)]
    found += package_blockers(obs)
    found += [f"the transaction would {op} {name}" for op, name, _ in tx if op not in ("Inst", "Conf")]
    if obs.policy_rc_d_exists:
        found.append(f"{POLICY} already exists; it belongs to the VM's administrator")
    if obs.backup_free_bytes < obs.incus_bytes + SPACE_MARGIN:
        found.append(f"{BACKUP_DIR} lacks room for a backup of {INCUS_DIR}")
    return found


# The unit states apply can restore exactly.
UNIT_ACTIVE, UNIT_ENABLED = ("active", "inactive"), ("enabled", "disabled")


def unit_blockers(obs: Observation) -> List[str]:
    """Units in a state apply couldn't put back exactly."""
    found = []
    for unit in UNITS:
        state = obs.units.get(unit)
        if not exact_unit(state):
            found.append(f"{unit} is {state.active + '/' + state.enabled if state else 'unread'}; "
                         "apply restores only active or inactive, enabled or disabled")
    return found


def exact_unit(state: Optional[Unit]) -> bool:
    """A unit state apply can restore exactly."""
    if state is None:
        return False
    return state.active in UNIT_ACTIVE and state.enabled in UNIT_ENABLED


def self_contained(pool: Dict[str, Optional[str]]) -> bool:
    """A dir pool whose configured and physical source both sit inside
    /var/lib/incus, so the full-server tar holds its data."""
    if pool.get("driver") != "dir":
        return False
    return inside_incus(pool.get("source")) and inside_incus(pool.get("resolved"))


def inside_incus(path: object) -> bool:
    """path, normalized, lies under /var/lib/incus."""
    return isinstance(path, str) and posixpath.normpath(path).startswith(INCUS_DIR + "/")


def package_blockers(obs: Observation) -> List[str]:
    found = []
    for name in PACKAGES:
        package = obs.packages.get(name) or Package()
        if package.installed is None or package.candidate is None:
            found.append(f"{name} has no installed or candidate version")
        elif package.origin != ORIGIN:
            found.append(f"{name} {package.candidate} comes from {package.origin}, not {ORIGIN}")
        elif obs.upgrades.get(name) is not True:
            found.append(f"{name} {package.candidate} isn't at least the installed {package.installed}")
    incus = obs.packages.get("incus")
    target = parse_version(expected_server(incus.candidate)) if incus and incus.candidate else None
    if target is None or not supported(target):
        found.append("the candidate Incus isn't a supported server version")
    return found


def expected_server(candidate: str) -> str:
    """The upstream Incus version a package version carries: 1:7.5-1 is 7.5."""
    upstream = candidate.split(":", 1)[-1]
    match = re.match(r"[0-9]+\.[0-9]+(?:\.[0-9]+)?", upstream)
    return match.group(0) if match else ""


def plan(runner: Runner) -> dict:
    """The manifest of a planned upgrade: the pins, their transaction, and
    what the VM looks like now; blockers name why it can't go ahead."""
    obs = observe(runner)
    pins, tx = trusted_pins(runner, obs)
    incus = obs.packages.get("incus") or Package()
    return {"schema": MANIFEST_SCHEMA, "target": dict(TARGET), "server_version": obs.server_version,
            "expected_server": expected_server(incus.candidate or ""), "pins": pins,
            "transaction": [list(line) for line in tx], "holds": list(obs.holds),
            "units": {name: asdict(unit) for name, unit in obs.units.items()},
            "instances": obs.instances, "pools": obs.pools,
            "packages": {name: asdict(package) for name, package in obs.packages.items()},
            "blockers": blockers(obs, tx)}


def write_manifest(path, manifest: dict) -> None:
    """Write the manifest privately, never over an existing file or link."""
    data = (json.dumps(manifest, indent=2) + "\n").encode()
    descriptor = os.open(str(path), os.O_WRONLY | os.O_CREAT | os.O_EXCL | getattr(os, "O_NOFOLLOW", 0), 0o600)
    with os.fdopen(descriptor, "wb") as stream:
        stream.write(data)


def validate_manifest(data: object) -> dict:
    """The manifest as data apply may compare against, or UpgradeError."""
    if not known_schema(data):
        raise UpgradeError("not a pairtask Incus upgrade manifest of a known schema")
    if data.get("target") != TARGET:
        raise UpgradeError("the manifest is for another VM")
    pins = valid_pins(data.get("pins"))
    tx = valid_transaction(data.get("transaction"), pins)
    if data.get("blockers") != []:
        raise UpgradeError("the plan has blockers; resolve them and plan again")
    return {**data, "pins": pins, "transaction": tx}


def known_schema(data: object) -> bool:
    """A dict whose schema is exactly this one, as an int (never True)."""
    if not isinstance(data, dict):
        return False
    schema = data.get("schema")
    return type(schema) is int and schema == MANIFEST_SCHEMA


def describe(manifest: dict) -> List[str]:
    """A plan as the operator reads it; the manifest file has all of it."""
    lines = [f"Incus server {manifest.get('server_version')} -> {manifest.get('expected_server')}"]
    lines += [f"  {op} {name} {version}" for op, name, version in manifest.get("transaction", [])]
    running = [i for i in manifest.get("instances", []) if i.get("status") == "Running"]
    lines.append(f"Instances: {len(manifest.get('instances', []))}, running: {len(running)}")
    lines += [f"Pool {p.get('name')}: {p.get('driver')} at {p.get('resolved')}" for p in manifest.get("pools", [])]
    return lines


def valid_pins(pins: object) -> Dict[str, str]:
    if not isinstance(pins, dict):
        raise UpgradeError("the manifest has no pins")
    for name, version in pins.items():
        if not pin_ok(name, version):
            raise UpgradeError(f"the manifest pins {name!r} badly")
    if not set(PACKAGES) <= set(pins):
        raise UpgradeError("the manifest doesn't pin every Incus package")
    return dict(pins)


def pin_ok(name: object, version: object) -> bool:
    """A package name and version as apt writes them, nothing else."""
    if not (isinstance(name, str) and isinstance(version, str)):
        return False
    return bool(NAME.fullmatch(name)) and bool(VERSION.fullmatch(version))


def valid_transaction(tx: object, pins: Dict[str, str]) -> List[TxLine]:
    if not isinstance(tx, list):
        raise UpgradeError("the manifest has no transaction")
    return [transaction_line(line, pins) for line in tx]


def transaction_line(line: object, pins: Dict[str, str]) -> TxLine:
    """One manifest transaction line: an Inst or Conf of a pinned version."""
    op, name, version = line_words(line)
    if op not in ("Inst", "Conf") or pins.get(name) != version:
        raise UpgradeError(f"the manifest's transaction would {op} {name} outside its pins")
    return op, name, version


def line_words(line: object) -> TxLine:
    """A transaction line's three strings, or UpgradeError."""
    if not isinstance(line, (list, tuple)) or len(line) != 3:
        raise UpgradeError("the manifest's transaction is malformed")
    if not all(isinstance(word, str) for word in line):
        raise UpgradeError("the manifest's transaction is malformed")
    return line[0], line[1], line[2]


def apply(runner: Runner, manifest: object, yes: bool) -> List[Step]:
    """Upgrade as planned, after re-deriving and re-checking everything; the
    steps it ran, or UpgradeError with them. Never restores, downgrades or
    deletes on its own."""
    if not yes:
        raise UpgradeError("apply changes the VM's Incus; review the plan, then pass --yes")
    planned = validate_manifest(manifest)
    run = Upgrade(runner, planned)
    try:
        run.prepare()
        run.upgrade()
    except UpgradeError as err:
        raise UpgradeError(str(err), run.steps) from err
    except (subprocess.SubprocessError, OSError) as err:
        raise UpgradeError(f"{type(err).__name__}: {err}", run.steps) from err
    except KeyboardInterrupt as err:
        raise UpgradeInterrupted("interrupted; cleanup ran", run.steps) from err
    return run.steps


def supported_text(found: Optional[str]) -> bool:
    """A server version string pairtask supports."""
    version = parse_version(found)
    return version is not None and supported(version)


def restorable(enabled: str) -> bool:
    """An enablement apply can put back with systemctl enable or disable."""
    return enabled in ("enabled", "disabled")


def archived(listing: str, members: List[str]) -> bool:
    """The archive lists the database and every member asked for."""
    names = {line.rstrip("/") for line in listing.splitlines()}
    wanted = [INCUS_DIR.lstrip("/") + "/database", *members]
    return all(name.rstrip("/") in names for name in wanted)


class Upgrade:
    """One apply: its steps, and what it found, to put back."""

    def __init__(self, runner: Runner, planned: dict):
        self.runner, self.planned, self.steps = runner, planned, []
        self.obs = Observation()
        self.policy_tried = False
        self.backup_path = ""
        # Our policy-rc.d carries this run's own nonce, so cleanup removes
        # only the file this run wrote, never an administrator's.
        self.policy_body = POLICY_BODY.format(nonce=secrets.token_hex(16))
        self.expected = ""

    def step(self, name: str, ok: bool, detail: str = "") -> None:
        self.steps.append(Step(name, ok, detail))
        if not ok:
            raise UpgradeError(f"{name}: {detail}")

    def prepare(self) -> None:
        """Refresh the lists, then require the plan to hold exactly as reviewed."""
        update = self.runner.vm(["apt-get", "update"], APT)
        self.step("apt-get update", update.returncode == 0, update.stderr.strip())
        pins, tx = trusted_pins(self.runner, observe(self.runner))
        # The simulations take a while: everything below is read again after them.
        self.obs = observe(self.runner)
        same = pins == self.planned["pins"] and tx == self.planned["transaction"]
        self.step("transaction as planned", same, "" if same else "the packages changed since the plan; plan again")
        self.expected = expected_server(pins.get("incus", ""))
        agreed = self.expected == self.planned.get("expected_server")
        self.step("server version as planned", agreed, "" if agreed else "the manifest's expected server disagrees with its pins")
        found = blockers(self.obs, tx)
        self.step("no blockers", not found, "; ".join(found))
        changed = [key for key in SNAPSHOT if self.snapshot()[key] != self.planned.get(key)]
        self.step("VM as planned", not changed, "changed since the plan: " + ", ".join(changed) if changed else "")

    def snapshot(self) -> dict:
        obs = self.obs
        return {"server_version": obs.server_version, "holds": list(obs.holds),
                "units": {name: asdict(unit) for name, unit in obs.units.items()},
                "instances": obs.instances, "pools": obs.pools,
                "packages": {name: asdict(package) for name, package in obs.packages.items()}}

    def upgrade(self) -> None:
        try:
            self.stop()
            self.backup_path = self.backup()
            self.install()
        finally:
            self.cleanup()
        failed = [step.name for step in self.steps if step.name in CLEANUP and not step.ok]
        if failed:
            raise UpgradeError("cleanup didn't finish: " + ", ".join(failed))
        try:
            self.verify(self.backup_path)
        except BaseException:
            self.settle_units(failing=True)  # recorded; the verification failure is what's raised
            raise
        self.settle_units(failing=False)

    def stop(self) -> None:
        result = self.runner.vm(["systemctl", "stop", *reversed(UNITS)], STOP)
        self.step("stop Incus", result.returncode == 0, result.stderr.strip())
        running = [f"{unit} {state}" for unit, state in ((u, unit_state(self.runner, u).active) for u in UNITS)
                   if state != "inactive"]
        self.step("Incus stopped", not running, "not inactive: " + ", ".join(running) if running else "")

    def backup(self) -> str:
        directory = vm_out(self.runner, ["mktemp", "-d", f"{BACKUP_DIR}/pairtask-incus-XXXXXXXXXX"], QUERY).strip()
        reserved = BACKUP_RESERVED.fullmatch(directory) is not None
        self.step("reserve a new backup directory", reserved, directory)
        path = f"{directory}/incus.tar.gz"
        members = [INCUS_DIR.lstrip("/")] + [f for f in ("etc/subuid", "etc/subgid")
                                             if self.runner.vm(["test", "-e", "/" + f], QUERY).returncode == 0]
        made = self.runner.vm(["tar", "--numeric-owner", "--xattrs", "--acls", "-czpf", path, "-C", "/", *members], TAR)
        self.step("back up " + INCUS_DIR, made.returncode == 0, made.stderr.strip() or path)
        listed = self.runner.vm(["tar", "-tzf", path], TAR)
        complete = listed.returncode == 0 and archived(listed.stdout, members)
        self.step("backup readable", complete, path)
        return path

    def install(self) -> None:
        # A file carrying this run's nonce is ours, even if creating it
        # reported a failure partway.
        self.policy_tried = True
        made = self.runner.vm(["sh", "-c", f"set -C; printf '{self.policy_body}' > {POLICY} && chmod 0755 {POLICY}"], QUERY)
        self.step("hold off service starts", made.returncode == 0, made.stderr.strip())
        unhold = self.runner.vm(["apt-mark", "unhold", *PACKAGES], QUERY)
        self.step("unhold Incus packages", unhold.returncode == 0, unhold.stderr.strip())
        pins = pin_args(self.planned["pins"])
        done = self.runner.vm(["apt-get", "install", "-y", "--no-remove", *pins], APT)
        self.step("install " + " ".join(pins), done.returncode == 0, done.stderr.strip())

    def cleanup(self) -> None:
        """Put back what apply found, each part on its own; failures are steps."""
        for name, action in zip(CLEANUP, (self.remove_policy, self.restore_holds, self.restore_units)):
            try:
                action()
                self.steps.append(Step(name, True))
            except (UpgradeError, subprocess.SubprocessError, OSError) as err:
                self.steps.append(Step(name, False, str(err)))

    def remove_policy(self) -> None:
        if not self.policy_tried:
            return
        if self.runner.vm(["test", "-e", POLICY], QUERY).returncode != 0:
            return
        if self.runner.vm(["cat", POLICY], QUERY).stdout != self.policy_body:
            raise UpgradeError(f"{POLICY} isn't the one apply wrote; left in place")
        vm_out(self.runner, ["rm", "-f", POLICY], QUERY)

    def restore_holds(self) -> None:
        """Unhold the Incus packages that weren't held and re-hold those that
        were, each attempted on its own, then read the holds back."""
        held = [name for name in PACKAGES if name in self.obs.holds]
        unheld = [name for name in PACKAGES if name not in held]
        # An install can add a hold; the storage tools' holds stay as they are.
        failed = [error for error in (self.hold_change("unhold", unheld), self.hold_change("hold", held)) if error]
        now = set(vm_out(self.runner, ["apt-mark", "showhold"], QUERY).split())
        if now & set(PACKAGES) != set(held):
            failed.append(f"Incus holds are {sorted(now & set(PACKAGES))}, were {held}")
        if failed:
            raise UpgradeError("; ".join(failed))

    def hold_change(self, verb: str, names: List[str]) -> str:
        """apt-mark VERB names, or why it failed; "" when done or nothing to do."""
        if not names:
            return ""
        try:
            vm_out(self.runner, ["apt-mark", verb, *names], QUERY)
        except (UpgradeError, subprocess.SubprocessError, OSError) as err:
            return f"apt-mark {verb}: {err}"
        return ""

    def restore_units(self) -> None:
        """Each unit's enablement and activity on its own: one failing never
        keeps the others from being restored."""
        failed = []
        for argv, timeout in self.unit_restores():
            try:
                vm_out(self.runner, argv, timeout)
            except (UpgradeError, subprocess.SubprocessError, OSError) as err:
                failed.append(f"{' '.join(argv)}: {err}")
        failed += self.unit_mismatches()
        if failed:
            raise UpgradeError("; ".join(failed))

    def unit_mismatches(self) -> List[str]:
        """What systemctl says now that differs from what apply found: a
        unit is only reported restored once systemctl confirms it."""
        mismatched = []
        for unit in UNITS:
            want = self.obs.units.get(unit) or Unit()
            try:
                now = unit_state(self.runner, unit)
            except (subprocess.SubprocessError, OSError) as err:
                mismatched.append(f"{unit}: state unreadable ({err})")
                continue
            if (now.active, now.enabled) != (want.active, want.enabled):
                mismatched.append(f"{unit} is {now.active}/{now.enabled}, was {want.active}/{want.enabled}")
        return mismatched

    def unit_restores(self) -> List[Tuple[List[str], float]]:
        """Enablement, then the units as they were: started socket first,
        stopped service first."""
        found = {unit: self.obs.units.get(unit) or Unit() for unit in UNITS}
        enable = [(["systemctl", state.enabled[:-1], unit], QUERY)
                  for unit, state in found.items() if restorable(state.enabled)]
        start = [(["systemctl", "start", unit], STOP) for unit, state in found.items() if state.active == "active"]
        stop = [(["systemctl", "stop", unit], STOP) for unit, state in reversed(list(found.items()))
                if state.active == "inactive"]
        return enable + stop + start

    def settle_units(self, failing: bool) -> None:
        """The version query may socket-activate a service that was
        inactive: put the units back as found, and confirm it. It runs
        whether or not the query succeeded, and on its own failure records
        the step without hiding a failure already raised."""
        try:
            mismatched = self.unit_mismatches()
            if mismatched:
                self.restore_units()
            ok, detail = True, ""
        except (UpgradeError, subprocess.SubprocessError, OSError) as err:
            ok, detail = False, str(err)
        self.steps.append(Step("units as found after the version check", ok, detail))
        if not ok and not failing:
            raise UpgradeError("units as found after the version check: " + detail)

    def verify(self, backup: str) -> None:
        found = server_version(incus_json(self.runner, ["query", TARGET["remote"] + ":/1.0"]))
        ok = found == self.expected and supported_text(found)
        self.step("server is " + self.expected, ok, "" if ok else f"server reports {found}; the backup is {backup}")
