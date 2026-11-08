"""The discovery helper as a user service (#284): a launchd agent on macOS,
a systemd --user unit on Linux, both running `$PAIRTASK_HOME/bin/pairtask
discovery run`. No root, no extra privileges.

`pairtask pairing on` installs and starts it, `pairing off` removes it and
an upgrade restarts it; `pairing on --no-discovery` opts out, remembered in
the deployment directory. A service manager that refuses is reported and
leaves the pairing route as configured.
"""

from collections import namedtuple
import json
import os
from pathlib import Path
import platform
import plistlib
import subprocess

LABEL = "ai.pairtask.discovery"
UNIT = "pairtask-discovery.service"
SETTING = "discovery.json"  # in the deployment directory: {"enabled": false} after --no-discovery
TIMEOUT = 20


def pairtask_home():
    return Path(os.environ.get("PAIRTASK_HOME") or "~/.pairtask").expanduser()


def remembered(directory):
    """Whether the helper runs with the route: true unless opted out."""
    try:
        return json.loads((Path(directory) / SETTING).read_text()).get("enabled", True) is not False
    except (OSError, ValueError, AttributeError):
        return True


def remember(directory, enabled):
    from sandbox_operator_state import private_write
    private_write(Path(directory) / SETTING, (json.dumps({"enabled": bool(enabled)}) + "\n").encode())


def systemd_word(value):
    """One ExecStart or Environment word, quoted for systemd."""
    escaped = value.replace("\\", "\\\\").replace('"', '\\"').replace("%", "%%").replace("$", "$$")
    return '"%s"' % escaped


# The host a Manager writes to: its system (default: this one's), the home
# directory (default: the user's) and the command runner (default: a real
# subprocess), so a test never reaches a real service manager.
Host = namedtuple("Host", "system home run", defaults=(None, None, None))


class Manager:
    """Writes, starts, restarts and removes the unit for this user."""

    def __init__(self, project="pairtask", remote="local", dry_run=False, host=None):
        host = host or Host()
        self.project, self.remote, self.dry_run = project, remote, dry_run
        self.system = host.system or platform.system()
        self.home = Path(host.home) if host.home else Path.home()
        self.run = host.run or self.execute

    def command(self):
        return [str(pairtask_home() / "bin" / "pairtask"), "discovery", "run",
                "--project", self.project, "--remote", self.remote]

    def environment(self):
        values = {"PATH": os.environ.get("PATH", "/usr/bin:/bin:/usr/sbin:/sbin")}
        if os.environ.get("PAIRTASK_HOME"):
            values["PAIRTASK_HOME"] = str(pairtask_home())
        return values

    def path(self):
        if self.system == "Darwin":
            return self.home / "Library/LaunchAgents" / (LABEL + ".plist")
        return self.home / ".config/systemd/user" / UNIT

    def text(self):
        if self.system == "Darwin":
            log = str(pairtask_home() / "discovery" / "helper.log")
            return plistlib.dumps({"Label": LABEL, "ProgramArguments": self.command(), "RunAtLoad": True,
                                   "KeepAlive": True, "ThrottleInterval": 30, "EnvironmentVariables": self.environment(),
                                   "StandardOutPath": log, "StandardErrorPath": log}).decode()
        lines = ["[Unit]", "Description=pairtask known-peer discovery", "",
                 "[Service]", "ExecStart=" + " ".join(systemd_word(word) for word in self.command()),
                 "Restart=on-failure", "RestartSec=30"]
        lines += ["Environment=" + systemd_word("%s=%s" % item) for item in sorted(self.environment().items())]
        return "\n".join(lines + ["", "[Install]", "WantedBy=default.target", ""])

    def domain(self):
        return "gui/%d" % os.getuid()

    def supported(self):
        if self.system in ("Darwin", "Linux"):
            return True
        print("Discovery helper: not supported on %s; the pairing route is unaffected." % self.system)
        return False

    def installed(self):
        """A supported system with the unit in place (or a dry run, which assumes it)."""
        return self.supported() and self.present()

    def present(self):
        return self.dry_run or self.path().exists()

    def install(self):
        """Write the unit and (re)start it."""
        if not self.supported():
            return False
        existed = self.path().exists()
        if self.dry_run:
            print("Would write %s:\n%s" % (self.path(), self.text()))
        else:
            (pairtask_home() / "discovery").mkdir(mode=0o700, parents=True, exist_ok=True)
            self.path().parent.mkdir(parents=True, exist_ok=True)
            self.path().write_text(self.text())
        if self.system == "Darwin":
            self.step(["launchctl", "bootout", self.domain() + "/" + LABEL], quiet=True)
            return self.step(["launchctl", "bootstrap", self.domain(), str(self.path())])
        started = (self.step(["systemctl", "--user", "daemon-reload"]) and
                   self.step(["systemctl", "--user", "enable", "--now", UNIT]))
        return started and (not existed or self.restart())

    def restart(self):
        """Restart an installed unit; nothing when none is installed."""
        if not self.installed():
            return False
        if self.system == "Darwin":
            return self.step(["launchctl", "kickstart", "-k", self.domain() + "/" + LABEL])
        return self.step(["systemctl", "--user", "restart", UNIT])

    def remove(self):
        """Stop and remove the unit; nothing when none is installed."""
        if not self.installed():
            return True
        if self.system == "Darwin":
            self.step(["launchctl", "bootout", self.domain() + "/" + LABEL], quiet=True)
        else:
            self.step(["systemctl", "--user", "disable", "--now", UNIT], quiet=True)
        if self.dry_run:
            print("Would remove %s" % self.path())
            return True
        self.path().unlink()
        if self.system != "Darwin":
            self.step(["systemctl", "--user", "daemon-reload"], quiet=True)
        return True

    def step(self, command, quiet=False):
        if self.dry_run:
            print("Would run: " + " ".join(command))
            return True
        ok, detail = self.run(command)
        if not ok and not quiet:
            print("Discovery helper: %s failed (%s); the pairing route is unaffected. "
                  "See docs/pairing.md#moving-between-networks." % (" ".join(command[:3]), detail))
        return ok

    def execute(self, command):
        try:
            result = subprocess.run(command, capture_output=True, text=True, timeout=TIMEOUT, check=False)
        except (OSError, subprocess.SubprocessError) as error:
            return False, str(error)
        lines = result.stderr.strip().splitlines()
        return result.returncode == 0, lines[-1] if lines else "exit %d" % result.returncode


def sync(wanted, project, remote, manager=None):
    """After a rollout: the helper installed and (re)started when wanted,
    else removed. Reports, never raises for a service manager."""
    manager = manager or Manager(project, remote)
    if not wanted:
        if manager.path().exists():
            manager.remove()
            print("Discovery helper: stopped and removed.", flush=True)
        return
    if manager.install():
        print("Discovery helper: running as a user service (%s)." % manager.path(), flush=True)
