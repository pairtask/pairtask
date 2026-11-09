#!/usr/bin/env python3
"""pairtask discovery: find paired workspaces again after an address change (#284).

`run` asks the broker for its lease, one-shot, then advertises this host's
pairing port and browses the LAN and tailnet only as far as the lease
allows, and hands what it found to the broker, which moves a contact only
where that contact's pinned identity answers. While the operator keeps an
invitation discoverable, the lease names it and the advert carries that
name; while the operator looks for colleagues, the helper reports the
named invitations it hears and the online tailnet devices with each lease
request. It stops advertising and browsing when the lease ends, by its own
clock. `status` shows the last lease, advert, report and outcomes; nothing
in it is secret.

Operator operations are one-shot, never a session: `--socket PATH` speaks
to a broker's socket directly (Linux direct, tests); otherwise each goes
through `incus exec` into secured, as the pairing route proof does.
"""

import argparse
from collections import namedtuple
import json
import os
from pathlib import Path
import re
import shutil
import signal
import socket
import subprocess
import sys
import threading
import time
import uuid

HERE = str(Path(__file__).resolve().parent)
if HERE not in sys.path:
    sys.path.insert(0, HERE)  # loaded by path (tests, runpy): its modules sit beside it

import peer_discovery as engine  # noqa: E402
from sandbox_remote import default_remote  # noqa: E402

SECURED_OPERATOR = "/run/collab-ai-operator/operator.sock"
WINDOW = 3.0          # seconds a --once run advertises and listens
SUBMIT_GAP = 30       # seconds between two submissions, at least
# The operator CLI waits at most 30s; the broker bounds a candidates
# operation to about 24s (8 probes of 3s), so both fit.
CALL_TIMEOUT = {"discovery": 15, "candidates": 30}
ANSWER_MAX = 1 << 20
MACOS_TAILSCALE = "/Applications/Tailscale.app/Contents/MacOS/Tailscale"


# What a Discovery works with: the operator transport, the published pairing
# port (0: never advertise), the status file, the tailscale CLI (or None) and
# the mDNS group and port.
Options = namedtuple("Options", "operator host_port status tailscale group port")


class OperatorError(Exception):
    """An operator operation that failed or answered without its result."""


RESULTS = {"discovery": "contact_discovery", "candidates": "contact_candidates_result"}


def answer_of(text, op):
    """The JSON answer of an operator operation, refused without its result."""
    answer = json_answer(text, op)
    field = RESULTS[op]
    if not isinstance(answer, dict) or field not in answer:
        raise OperatorError("operator %s: %s" % (op, answer_error(answer) or "answer without " + field))
    return answer


def json_answer(text, op):
    try:
        return json.loads(text)
    except ValueError:
        raise OperatorError("operator %s: no JSON answer" % op) from None


def answer_error(answer):
    return answer.get("error") if isinstance(answer, dict) else None


class DirectOperator:
    """One JSON line to the broker's operator socket and one back, then close."""

    def __init__(self, path):
        self.path = path

    def call(self, op, candidates=None, report=None):
        request = {"type": "operator_contact", "contact_op": op, "message_id": "discovery-" + uuid.uuid4().hex}
        if candidates:
            request["contact_candidates"] = list(candidates)
        request.update(report_fields(report))
        with socket.socket(socket.AF_UNIX, socket.SOCK_STREAM) as connection:
            connection.settimeout(CALL_TIMEOUT[op])
            connection.connect(self.path)
            connection.sendall(json.dumps(request).encode() + b"\n")
            line = connection.makefile("rb").readline(ANSWER_MAX)
        return answer_of(line.decode("utf-8", "replace"), op)


class IncusOperator:
    """`pairtask operator contact OP … --json` in secured, through incus exec."""

    def __init__(self, project, remote):
        self.base = ["incus", "--project", project, "exec", remote.rstrip(":") + ":secured", "-T", "--",
                     "pairtask", "operator", "contact"]

    def command(self, op, candidates=None, report=None):
        # The CLI's own default timeout (5s) would end a candidates call
        # while its probes still run, so it waits as long as this side does.
        fields = report_fields(report)
        extra = ["--report", json.dumps(fields, separators=(",", ":"))] if fields else []
        return self.base + [op] + list(candidates or []) + extra + ["--json", "--socket", SECURED_OPERATOR,
                                                                    "--timeout", "%ds" % CALL_TIMEOUT[op]]

    def call(self, op, candidates=None, report=None):
        result = subprocess.run(self.command(op, candidates, report), capture_output=True, text=True,
                                timeout=CALL_TIMEOUT[op] + 15, check=False)
        if result.returncode != 0:
            lines = result.stderr.strip().splitlines()
            raise OperatorError("operator %s: %s" % (op, lines[-1] if lines else "exit %d" % result.returncode))
        return answer_of(result.stdout, op)


def report_fields(report):
    """The helper's observations as request fields. Each request replaces
    the broker's whole snapshot, so an empty list is sent as such: it is
    how the last invitation heard, or device seen, disappears there."""
    names = {"found": "contact_found", "devices": "contact_devices", "announcing": "contact_announcing"}
    return {names[key]: value for key, value in (report or {}).items() if key in names and value is not None}


class Status:
    """status.json in the state directory: the last lease, advert and outcomes."""

    def __init__(self, directory):
        self.path = Path(directory) / "status.json"
        self.data = {"lease": None, "advertising": None, "browsing": False, "submission": None, "error": None}
        self.warned = False

    def update(self, **values):
        self.data.update(values)
        self.data["updated_at"] = stamp(time.time())
        try:
            self.path.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
            temporary = self.path.with_name(self.path.name + ".tmp")
            temporary.write_text(json.dumps(self.data, indent=2) + "\n")
            os.replace(str(temporary), str(self.path))
        except OSError as error:
            if not self.warned:
                log("cannot write %s: %s" % (self.path, error))
            self.warned = True


def stamp(seconds):
    return time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime(seconds))


def log(text):
    print("pairtask discovery: " + text, file=sys.stderr, flush=True)


class Worker:
    """One operator call at a time, off the packet loop: a call that hangs
    never keeps an ended lease advertising."""

    def __init__(self):
        self.thread, self.name, self.outcome = None, None, None

    def busy(self):
        return self.thread is not None

    def start(self, name, function):
        def target():
            try:
                self.outcome = (function(), None)
            except Exception as error:  # reported by take(), in the loop's thread
                self.outcome = (None, error)
        self.name, self.outcome = name, None
        self.thread = threading.Thread(target=target, daemon=True)
        self.thread.start()

    def take(self):
        """(name, result, error) once the call has ended, else None."""
        if self.thread is None or self.thread.is_alive():
            return None
        self.thread = None
        return (self.name,) + self.outcome


class Discovery:
    """The lease and what it allows: one session, advertising the host's
    pairing port when the lease says so and the port is published, browsing
    when it says so; nothing once it ends."""

    def __init__(self, options):
        self.operator, self.status, self.tailscale = options.operator, options.status, options.tailscale
        self.group, self.port = options.group, options.port
        self.advert = engine.Advert(options.host_port) if options.host_port > 0 else None
        self.lease, self.session, self.browse_since = None, None, None
        self.batches = engine.Batches()

    def ask_lease(self, report=None):
        """The lease, with this helper's report (built on the loop's thread);
        the tailnet is read here, off the loop, when the lease asks for it."""
        report = dict(report or {})
        if self.lease is not None and self.lease.find and self.tailscale:
            report["devices"] = engine.tailnet_devices(engine.tailscale_status(self.tailscale) or {})
        self.status.update(report=report)
        asked = time.monotonic()
        answer = self.operator.call("discovery", report=report)
        return engine.lease_from(answer, asked, time.monotonic(), time.time())

    def report(self):
        """What the loop knows now: the invitations heard, and whether the
        announcement the lease asked for has left this host."""
        now, report = time.monotonic(), {}
        if self.session is not None and self.session.browser is not None:
            found = self.session.browser.invitations(now)
            for entry in found:
                entry["seen_at"] = stamp(time.time() - (now - entry["seen_at"]))
            report["found"] = found
        if self.lease is not None and self.lease.invitation is not None and self.advert is not None:
            sent = self.session is not None and self.session.sent_named()
            report["announcing"] = {"name": self.lease.invitation, "sent": sent, "error": self.status.data.get("error")}
        return report

    def apply(self, lease):
        self.lease = lease
        shown = None
        if lease is not None:
            shown = {"advertise": lease.advertise, "browse": lease.browse, "ports": lease.ports,
                     "invitation": lease.invitation, "find": lease.find,
                     "expires_at": lease.expires_at, "received_at": stamp(time.time())}
        self.status.update(lease=shown, error=None)
        self.reconcile(time.monotonic())

    def active(self, now):
        return self.lease is not None and now < self.lease.deadline

    def browsing(self, now):
        """Whether the current lease asks to browse for moved contacts."""
        return self.active(now) and self.lease.browse

    def wanted(self, now):
        """(advertise, browse) as the lease allows them now: a discoverable
        invitation advertises, looking for colleagues browses."""
        if not self.active(now):
            return False, False
        lease = self.lease
        return (lease.advertise or lease.invitation is not None) and self.advert is not None, lease.browse or lease.find

    def expire(self, now):
        """Close the session the moment the lease ends."""
        if self.session is not None and not self.active(now):
            self.reconcile(now)

    def reconcile(self, now):
        """Open, change or close the session to match the lease as of now."""
        advertise, browse = self.wanted(now)
        if not (advertise or browse):
            self.close()
            return
        if self.session is None and not self.open():
            return
        self.track_browsing(browse, now)
        self.session.update(advertise, browse, self.lease.invitation)
        self.show(advertise, browse)

    def track_browsing(self, browse, now):
        if not browse:
            self.browse_since = None
        elif self.browse_since is None:
            self.browse_since = now

    def submission_open(self, now, next_submit):
        """Browsing for a whole window, and the gap since the last submission over."""
        return self.browse_since is not None and now >= max(next_submit, self.browse_since + WINDOW)

    def show(self, advertise, browse):
        shown = self.advert_status() if advertise else None
        if self.shown_changed(shown, browse):
            self.status.update(advertising=shown, browsing=browse)
            log("advertising %s, browsing %s" % ("port %d" % self.advert.port if advertise else "no", yes(browse)))

    def shown_changed(self, shown, browse):
        data = self.status.data
        return shown != data.get("advertising") or data.get("browsing") != browse

    def open(self):
        try:
            self.session = engine.Session(self.advert, self.group, self.port)
            return True
        except OSError as error:
            message = "mDNS unavailable on %s:%d: %s" % (self.group, self.port, error)
            if self.status.data.get("error") != message:
                log(message)
            self.status.update(error=message)
            return False

    def advert_status(self):
        return {"instance": self.advert.instance, "host": self.advert.host, "port": self.advert.port,
                "addresses": list(self.advert.addresses), "name": self.advert.invitation_name}

    def lan(self):
        return self.session.candidates() if self.session is not None else []

    def submit(self, lan, ports):
        """Tailnet names and addresses, then the LAN's endpoints, to the broker; runs off the loop."""
        status = engine.tailscale_status(self.tailscale) if self.tailscale else None
        candidates = self.batches.next(engine.tailnet_inventory(status or {}, ports), list(lan))
        if not candidates:
            return None
        answer = self.operator.call("candidates", candidates)
        return {"at": stamp(time.time()), "candidates": candidates,
                "results": answer.get("contact_candidates_result")}

    def submitted(self, result, error):
        if error is not None:
            log(str(error))
            self.status.update(error=str(error))
        elif result is not None:
            outcomes = ", ".join("%s %s" % (r.get("candidate"), r.get("outcome")) for r in result["results"] or []
                                 if isinstance(r, dict))
            log("submitted %d: %s" % (len(result["candidates"]), outcomes or "no outcomes"))
            self.status.update(submission=result, error=None)

    def close(self):
        if self.session is not None:
            self.session.stop()
            self.session = None
            log("stopped advertising and browsing")
        self.browse_since = None
        if self.status.data.get("advertising") or self.status.data.get("browsing"):
            self.status.update(advertising=None, browsing=False)


def run_once(discovery):
    """One lease, one window, at most one submission; 0 also when the lease
    is off or expired, 1 when the broker could not be asked or advertising
    was due but no datagram left."""
    try:
        lease = discovery.ask_lease(discovery.report())
    except (OperatorError, OSError, subprocess.SubprocessError) as error:
        log(str(error))
        discovery.status.update(lease=None, error=str(error))
        return 1
    discovery.apply(lease)
    due = discovery.wanted(time.monotonic())[0]
    lan = window(discovery, min(time.monotonic() + WINDOW, lease.deadline) if lease else 0)
    silent = due and advertised_nothing(discovery)
    discovery.close()
    if discovery.browsing(time.monotonic()):
        submit_once(discovery, lan, lease.ports)
    return 1 if silent else 0


def advertised_nothing(discovery):
    """With advertising due, no session could be opened, or not one datagram
    left it: the network refused them."""
    session = discovery.session
    return session is None or session.channel.sent == 0


def window(discovery, end):
    """Advertise and listen until end; what the LAN answered."""
    while discovery.session is not None and time.monotonic() < end:
        discovery.session.step(min(0.25, max(0.0, end - time.monotonic())))
    return discovery.lan()


def submit_once(discovery, lan, ports):
    try:
        discovery.submitted(discovery.submit(lan, ports), None)
    except (OperatorError, OSError, subprocess.SubprocessError) as error:
        discovery.submitted(None, error)


class Loop:
    """The lease every interval (sooner when it nears its end), at most one
    submission per SUBMIT_GAP while browsing, and the session closed the
    moment the lease ends, whatever a call is doing."""

    def __init__(self, discovery, interval):
        self.discovery, self.interval = discovery, interval
        self.worker, self.next_lease, self.next_submit = Worker(), 0.0, 0.0

    def tick(self, stopping):
        now = time.monotonic()
        self.discovery.expire(now)
        self.dispatch(now)
        if self.discovery.session is not None:
            self.discovery.session.step(0.25)
        else:
            stopping.wait(0.25)

    def dispatch(self, now):
        """Take an ended call, or start the next one that is due."""
        done = self.worker.take()
        if done is not None:
            self.next_lease = finish(self.discovery, done, self.interval, self.next_lease)
        elif self.worker.busy():
            return
        elif now >= self.next_lease:
            self.worker.start("lease", lambda report=self.discovery.report(): self.discovery.ask_lease(report))
            self.next_lease = now + self.interval
        elif self.submission_due(now):
            self.start_submission(now)

    def submission_due(self, now):
        return self.discovery.browsing(now) and self.discovery.submission_open(now, self.next_submit)

    def start_submission(self, now):
        discovery = self.discovery
        self.worker.start("candidates", lambda lan=discovery.lan(), ports=discovery.lease.ports:
                          discovery.submit(lan, ports))
        self.next_submit = now + SUBMIT_GAP


def run_loop(discovery, interval, stopping):
    """Until stopped, then close with a goodbye."""
    loop = Loop(discovery, interval)
    try:
        while not stopping.is_set():
            loop.tick(stopping)
    finally:
        discovery.close()
    return 0


def finish(discovery, done, interval, next_lease):
    """Apply an ended call; the next lease request comes before this one ends."""
    name, result, error = done
    if name != "lease":
        discovery.submitted(result, error)
        return next_lease
    if error is not None:
        log(str(error))
        discovery.status.update(error=str(error))
        discovery.reconcile(time.monotonic())
        return next_lease
    discovery.apply(result)
    if result is not None and discovery.active(time.monotonic()):
        return min(next_lease, max(time.monotonic() + 5, result.deadline - 20))
    return next_lease


def tailscale_command():
    """PAIRTASK_DISCOVERY_TAILSCALE: a CLI path run as given, or none;
    by default tailscale on PATH, or the macOS app's CLI."""
    value = os.environ.get("PAIRTASK_DISCOVERY_TAILSCALE")
    if value is not None:
        return None if value in ("", "none") else value
    if shutil.which("tailscale"):
        return shutil.which("tailscale")
    return MACOS_TAILSCALE if os.path.isfile(MACOS_TAILSCALE) else None


def multicast_target():
    """The mDNS group and port: PAIRTASK_DISCOVERY_GROUP and _PORT, else 224.0.0.251:5353."""
    group = os.environ.get("PAIRTASK_DISCOVERY_GROUP") or engine.GROUP
    port = environment_port()
    if not valid_target(group, port):
        raise SystemExit("PAIRTASK_DISCOVERY_GROUP must be an IPv4 group and the port 1 to 65535")
    return group, port


def environment_port():
    try:
        return int(os.environ.get("PAIRTASK_DISCOVERY_PORT") or engine.PORT)
    except ValueError:
        raise SystemExit("PAIRTASK_DISCOVERY_PORT must be a port number") from None


def valid_target(group, port):
    return engine.is_port(port) and is_ipv4(engine.address(group))


def is_ipv4(ip):
    return ip is not None and ip.version == 4


def deployment_port(project):
    """The pairing port the deployment publishes: 0 when the route is off,
    not applied, or no deployment is found."""
    try:
        import sandbox_pairing
        directory = sandbox_pairing.find_deployment(None, project)
        if directory is None or not sandbox_pairing.configured(directory):
            return 0
        return sandbox_pairing.published_port(directory, project) or 0
    except (ImportError, OSError, ValueError, KeyError, subprocess.SubprocessError):
        return 0


def pairtask_home():
    return Path(os.environ.get("PAIRTASK_HOME") or "~/.pairtask").expanduser()


def seconds(value):
    match = re.fullmatch(r"([0-9]+)(s|m)?", value)
    total = int(match.group(1)) * (60 if match.group(2) == "m" else 1) if match else 0
    if not 5 <= total <= 3600:
        raise argparse.ArgumentTypeError("use 5s to 60m, like 30s or 1m")
    return total


def host_port(value):
    if not value.isdigit() or int(value) > 65535:
        raise argparse.ArgumentTypeError("use 0 (never advertise) or a port up to 65535")
    return int(value)


def identifier(value):
    if not re.fullmatch(r"[a-zA-Z0-9][a-zA-Z0-9_.-]*", value):
        raise argparse.ArgumentTypeError("Use a name without a colon, whitespace or shell syntax")
    return value


def arguments(argv=None):
    parser = argparse.ArgumentParser(prog="pairtask discovery", description=__doc__.split("\n\n")[0])
    actions = parser.add_subparsers(dest="action", required=True)
    run = actions.add_parser("run", help="the helper: lease, advertise or browse, submit")
    run.add_argument("--once", action="store_true", help="one lease, a few seconds of mDNS, at most one submission")
    run.add_argument("--interval", type=seconds, default=30, help="how often to ask for the lease (default 30s)")
    run.add_argument("--socket", help="the broker's operator socket, directly (default: incus exec into secured)")
    run.add_argument("--host-port", type=host_port, help="the published pairing port to advertise (0: never; default: the deployment's)")
    status = actions.add_parser("status", help="the last lease, advert and candidate outcomes")
    status.add_argument("--json", action="store_true")
    service = actions.add_parser("service", help="the user service pairing on/off manages: install | remove | restart")
    service.add_argument("change", choices=["install", "remove", "restart"])
    service.add_argument("--dry-run", action="store_true", help="print the unit and commands; change nothing")
    for each in (run, status):
        each.add_argument("--state-dir", type=Path, help="default: $PAIRTASK_HOME/discovery")
    for each in (run, service):
        each.add_argument("--project", default="pairtask", type=identifier)
        each.add_argument("--remote", default=default_remote(), type=identifier)
    args = parser.parse_args(argv)
    if args.action != "service":
        args.state_dir = args.state_dir or pairtask_home() / "discovery"
    return args


def run(args):
    group, port = multicast_target()
    operator = DirectOperator(args.socket) if args.socket else IncusOperator(args.project, args.remote)
    published = deployment_port(args.project) if args.host_port is None else args.host_port
    status = Status(args.state_dir)
    status.update(host_port=published, mode="direct" if args.socket else "incus")
    discovery = Discovery(Options(operator, published, status, tailscale_command(), group, port))
    if args.once:
        return run_once(discovery)
    stopping = threading.Event()
    for number in (signal.SIGTERM, signal.SIGINT, signal.SIGHUP):
        signal.signal(number, lambda *_: stopping.set())
    return run_loop(discovery, args.interval, stopping)


def show(args):
    path = args.state_dir / "status.json"
    try:
        data = json.loads(path.read_text())
    except (OSError, ValueError):
        print("Discovery: no status yet in %s (pairtask discovery run writes it)" % path)
        return 1
    if args.json:
        print(json.dumps(data, indent=2))
        return 0
    print("\n".join(describe(data)))
    return 0


def describe(data):
    """The status lines: lease, advert, browsing, last submission, last error."""
    lines = ["Discovery: updated %s" % data.get("updated_at", "never"), lease_line(data.get("lease")),
             advert_line(data.get("advertising")), "Browsing: " + yes(data.get("browsing"))]
    lines += report_lines(data.get("report"))
    lines += submission_lines(data.get("submission"))
    if data.get("error"):
        lines.append("Last error: " + data["error"])
    return lines


def lease_line(lease):
    if not lease:
        return "Lease: none (nothing to advertise or look for)"
    ports = ", ".join(map(str, lease.get("ports") or [])) or "none"
    return "Lease: advertise %s, browse %s, ports %s, expires %s" % (
        yes(lease.get("advertise")), yes(lease.get("browse")), ports, lease.get("expires_at"))


def advert_line(advert):
    if not advert:
        return "Advertising: no"
    named = " as %r" % advert["name"] if advert.get("name") else ""
    return "Advertising: port %s%s on %s" % (advert.get("port"), named, ", ".join(advert.get("addresses") or []) or "no address")


def report_lines(report):
    """The last report to the broker: the announcement, invitations heard, tailnet devices."""
    if not report:
        return []
    lines = []
    announcing = report.get("announcing")
    if isinstance(announcing, dict):
        lines.append("Invitation %r: announced %s%s" % (announcing.get("name"), yes(announcing.get("sent")),
                                                         " (%s)" % announcing["error"] if announcing.get("error") else ""))
    found = [f for f in report.get("found") or [] if isinstance(f, dict)]
    lines += ["Invitations heard: %d" % len(found)] + ["  %r at %s:%s, seen %s" % (f.get("name"), f.get("host"), f.get("port"), f.get("seen_at")) for f in found]
    if report.get("devices") is not None:
        lines.append("Tailnet devices: %d" % len(report["devices"]))
    return lines


def submission_lines(submission):
    if not submission:
        return []
    results = [r for r in submission.get("results") or [] if isinstance(r, dict)]
    return ["Last submission %s:" % submission.get("at")] + [outcome_line(r) for r in results]


def outcome_line(result):
    contact = " (%s)" % result["contact"] if result.get("contact") else ""
    return "  %s %s%s" % (result.get("candidate"), result.get("outcome"), contact)


def yes(value):
    return "yes" if value else "no"


def service(args):
    import sandbox_discovery_service
    manager = sandbox_discovery_service.Manager(args.project, args.remote, dry_run=args.dry_run)
    getattr(manager, args.change)()
    return 0


def main(argv=None):
    args = arguments(argv)
    return {"run": run, "status": show, "service": service}[args.action](args)


if __name__ == "__main__":
    try:
        sys.exit(main())
    except KeyboardInterrupt:
        sys.exit(130)
