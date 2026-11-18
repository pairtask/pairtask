"""The tools lane: what a client-only change to infra/image/tools.lock.json
is checked against in a pull request, in minutes and without image builds.

For every tool with a publisher checksum lookup, the lock's checksum is
compared with the publisher's, for every architecture in the lock. The agent
clients (claude, codex, cs-mcp) are then downloaded for this machine's
architecture, verified against the lock, and run: each reports the lock's
version, the Claude subcommand list matches the launcher's pass-through list
in pairtask-agent-aliases.sh, and Codex's App Server, started with the
arguments pairtask-codex uses and the image's config, answers an initialize
request. The image proofs on the merge to main stay the final check, and the
relay's own limits (#408) are the Go tests' business in internal/host; this
lane catches a wrong checksum, a broken download, a renamed subcommand or a
client that no longer starts or initializes, before any image is built.

Findings are sentences naming the tool and the architecture. Nothing from a
publisher's response reaches the output but what the checks compare.
"""

import hashlib
import json
import os
from pathlib import Path, PurePosixPath
import re
import select
import shutil
import subprocess
import sys
import tarfile
import time
import urllib.request
import zipfile

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT / "infra/image"))
from tool_lock import artifact  # noqa: E402
from sandbox_https import urlopen  # noqa: E402
import sandbox_versions as versions  # noqa: E402

LOCK = ROOT / "infra/image/tools.lock.json"
ALIASES = ROOT / "infra/image/pairtask-agent-aliases.sh"
CODEX_CONFIG = ROOT / "infra/image/codex-config.toml"
# The agent clients: downloaded, verified and run. Other tools are checked
# against their publishers only.
CLIENTS = ("claude", "codex", "cs-mcp")
DOWNLOAD_TIMEOUT = 300
RUN_TIMEOUT = 60


class LaneError(ValueError):
    """One finding about one tool."""


def checksum_findings(lock, resolve=versions.resolver):
    """Lock checksums that differ from the publisher's, for every tool with a
    lookup and every architecture in the lock."""
    cells = [(name, spec, machine) for name, spec in lock["artifacts"].items() if resolve(name) for machine in spec["architectures"]]
    findings = [checksum_finding(name, spec, machine, resolve(name)) for name, spec, machine in cells]
    return [finding for finding in findings if finding]


def checksum_finding(name, spec, machine, lookup):
    version, per_arch = spec["version"], spec["architectures"][machine]
    try:
        published = lookup(version, per_arch["arch"])
    except (ValueError, KeyError, OSError) as error:
        return "{} {} for {}: no publisher checksum ({})".format(name, version, machine, error)
    if published != per_arch["sha256"]:
        return "{} {} for {}: the lock's checksum differs from the publisher's".format(name, version, machine)
    return None


def download(item, directory):
    """Fetch one artifact into directory under its URL's file name and verify
    the lock's checksum while writing; a mismatch leaves no file behind."""
    target = Path(directory) / PurePosixPath(item["url"]).name
    digest = hashlib.sha256()
    request = urllib.request.Request(item["url"], headers={"User-Agent": "collab-ai-tools-lane"})
    with urlopen(request, timeout=DOWNLOAD_TIMEOUT) as response, open(target, "wb") as out:
        for chunk in iter(lambda: response.read(1 << 20), b""):
            digest.update(chunk)
            out.write(chunk)
    if digest.hexdigest() != item["sha256"]:
        target.unlink()
        raise LaneError("the download doesn't match the lock's checksum")
    return target


def unpack(item, archive, directory):
    """The tool's entry from its verified artifact, executable: a binary as
    is, or the one member of an archive or zip that ends in the entry."""
    out = Path(directory) / item["name"] / PurePosixPath(item["entry"]).name
    out.parent.mkdir(parents=True, exist_ok=True)
    if item["kind"] == "binary":
        shutil.copyfile(archive, out)
    elif item["kind"] == "archive":
        with tarfile.open(archive) as tar:
            member = one_member(item, [m.name for m in tar.getmembers() if m.isfile()])
            with tar.extractfile(member) as src, open(out, "wb") as dst:
                shutil.copyfileobj(src, dst)
    elif item["kind"] == "zip":
        with zipfile.ZipFile(archive) as bundle:
            member = one_member(item, [n for n in bundle.namelist() if not n.endswith("/")])
            with bundle.open(member) as src, open(out, "wb") as dst:
                shutil.copyfileobj(src, dst)
    else:
        raise LaneError("artifact kind {} isn't run by the lane".format(item["kind"]))
    out.chmod(0o755)
    return out


def one_member(item, names):
    entry = item["entry"]
    found = [name for name in names if name == entry or name.endswith("/" + entry)]
    if len(found) != 1:
        raise LaneError("the artifact has {} members named {}, not one".format(len(found), entry))
    return found[0]


def help_subcommands(text):
    """The subcommand names of a Claude Code --help: the first word of each
    entry under "Commands:", alternatives split on |."""
    names = set()
    listing = False
    for line in text.splitlines():
        if line.startswith("Commands:"):
            listing = True
        elif listing and ends_section(line):
            break
        elif listing and re.match(r"  \S", line):
            names.update(line.split()[0].split("|"))
    return names


def ends_section(line):
    """A non-empty line at the margin starts the next section of a help."""
    return bool(line) and not line.startswith(" ")


def reports(version, text):
    """text names version as a whole token, not as the start of a longer one."""
    return re.search(r"(?<![\w.])" + re.escape(version) + r"(?![\w.])", text) is not None


def alias_subcommands(path=ALIASES):
    """The launcher's pass-through list: the case pattern under which the
    alias runs claude as typed."""
    match = re.search(r"^\s*([a-z][a-z0-9|-]*)\)\n\s*/usr/local/bin/claude \"\$@\" ;;", Path(path).read_text(), re.M)
    if not match:
        raise LaneError("pairtask-agent-aliases.sh has no pass-through case for claude subcommands")
    return set(match.group(1).split("|"))


def run_client(binary, args, home, timeout=RUN_TIMEOUT):
    """Run a client with a private home and nothing of this account's."""
    environment = {"HOME": str(home), "PATH": os.environ.get("PATH", "")}
    return subprocess.run([str(binary), *args], capture_output=True, text=True, timeout=timeout, env=environment, check=True)


def version_finding(item, binary, home):
    reported = run_client(binary, ["--version"], home).stdout
    if not reports(item["version"], reported):
        return "{} reports another version than the lock's {}".format(item["name"], item["version"])
    return None


def claude_findings(item, binary, home):
    findings = [version_finding(item, binary, home)]
    listed = help_subcommands(run_client(binary, ["--help"], home).stdout)
    passed = alias_subcommands()
    for name in sorted(listed - passed):
        findings.append("claude {} has subcommand {}, which the launcher would start a session with".format(item["version"], name))
    for name in sorted(passed - listed):
        findings.append("claude {} has no subcommand {}, which the launcher passes through".format(item["version"], name))
    return [finding for finding in findings if finding]


def codex_findings(item, binary, home):
    findings = [version_finding(item, binary, home)]
    result = codex_handshake(binary, home)
    if not reports(item["version"], result.get("userAgent", "")):
        findings.append("codex's App Server reports another version than the lock's {}".format(item["version"]))
    return [finding for finding in findings if finding]


def codex_handshake(binary, home, config=CODEX_CONFIG, timeout=RUN_TIMEOUT):
    """Start the App Server as pairtask-codex does and complete initialize
    with the image's config in a private CODEX_HOME; the answer's codexHome
    shows that config was the one read."""
    codex_home = Path(home) / "codex-home"
    codex_home.mkdir(exist_ok=True)
    shutil.copyfile(config, codex_home / "config.toml")
    environment = {"HOME": str(home), "PATH": os.environ.get("PATH", ""), "CODEX_HOME": str(codex_home)}
    request = {"jsonrpc": "2.0", "id": 1, "method": "initialize",
               "params": {"clientInfo": {"name": "pairtask-tools-lane", "version": "1"}, "capabilities": {"experimentalApi": True}}}
    with subprocess.Popen([str(binary), "app-server", "--listen", "stdio://"], stdin=subprocess.PIPE, stdout=subprocess.PIPE,
                          stderr=subprocess.DEVNULL, env=environment) as process:
        try:
            process.stdin.write((json.dumps(request) + "\n").encode())
            process.stdin.flush()
            answer = first_answer(process, timeout)
            process.stdin.write((json.dumps({"jsonrpc": "2.0", "method": "initialized"}) + "\n").encode())
            process.stdin.flush()
        finally:
            process.terminate()
            try:
                process.wait(5)
            except subprocess.TimeoutExpired:
                process.kill()
    if not accepted(answer):
        raise LaneError("the App Server didn't accept initialize")
    if answer["result"].get("codexHome") != str(codex_home):
        raise LaneError("the App Server didn't read the image's config")
    return answer["result"]


def accepted(answer):
    """The answer to request 1 carries a result and no error."""
    return "error" not in answer and isinstance(answer.get("result"), dict)


def first_answer(process, timeout):
    """The App Server's answer to request 1, skipping notifications it sends
    first. Reads never block past the deadline, a partial line included, and
    what arrives before the answer is bounded."""
    deadline = time.monotonic() + timeout
    buffered, preamble = b"", 0
    while True:
        line, buffered = next_line(process.stdout, buffered, deadline)
        frame = json.loads(line)
        if frame.get("id") == 1:
            return frame
        preamble += len(line) + 1
        if preamble > MAX_PREAMBLE:
            raise LaneError(TOO_MUCH_PREAMBLE)
        if time.monotonic() > deadline:
            raise LaneError(NO_ANSWER_IN_TIME)


# How much the App Server may send before answering initialize, across all
# the frames and the partial line it sends first.
MAX_PREAMBLE = 1 << 20
TOO_MUCH_PREAMBLE = "the App Server sent over {} bytes before answering initialize".format(MAX_PREAMBLE)
NO_ANSWER_IN_TIME = "the App Server didn't answer initialize in time"


def next_line(stream, buffered, deadline):
    """One complete line from stream, with what followed it; raises at the
    deadline, at end of stream, and past MAX_PREAMBLE without a newline."""
    while b"\n" not in buffered:
        ready, _, _ = select.select([stream], [], [], max(deadline - time.monotonic(), 0))
        if not ready or time.monotonic() > deadline:
            raise LaneError(NO_ANSWER_IN_TIME)
        chunk = os.read(stream.fileno(), 1 << 16)
        if not chunk:
            raise LaneError("the App Server closed without answering initialize")
        buffered += chunk
        if len(buffered) > MAX_PREAMBLE:
            raise LaneError(TOO_MUCH_PREAMBLE)
    line, _, rest = buffered.partition(b"\n")
    return line.decode("utf-8", "replace"), rest


def cs_mcp_findings(item, binary, home):
    """The MCP server has no protocol check in the lane; its --version proves
    the binary starts."""
    finding = version_finding(item, binary, home)
    return [finding] if finding else []


CLIENT_CHECKS = {"claude": claude_findings, "codex": codex_findings, "cs-mcp": cs_mcp_findings}


def client_findings(lock, machine, name, workdir):
    """Download, verify and run one client; every failure is one finding."""
    item = artifact(lock, name, machine)
    try:
        binary = unpack(item, download(item, workdir / "downloads"), workdir / "tools")
        return CLIENT_CHECKS[name](item, binary, workdir / "home")
    except (LaneError, OSError, KeyError, json.JSONDecodeError, tarfile.TarError, zipfile.BadZipFile) as error:
        return ["{} {} for {}: {}".format(name, item["version"], machine, error)]
    except subprocess.CalledProcessError as error:
        return ["{} {} for {}: {} exited {}".format(name, item["version"], machine, " ".join(error.cmd[1:]), error.returncode)]
    except subprocess.TimeoutExpired as error:
        return ["{} {} for {}: {} didn't finish within {}s".format(name, item["version"], machine, " ".join(error.cmd[1:]), RUN_TIMEOUT)]


def run(lock_path, machine, workdir, resolve=versions.resolver):
    """Every finding for the lock on this machine; an empty list passes."""
    lock = json.loads(Path(lock_path).read_text())
    if machine not in lock["architectures"]:
        raise LaneError("the lock has no architecture {}".format(machine))
    workdir = Path(workdir)
    for name in ("downloads", "tools", "home"):
        (workdir / name).mkdir(parents=True, exist_ok=True)
    findings = checksum_findings(lock, resolve)
    for name in CLIENTS:
        findings += client_findings(lock, machine, name, workdir)
    return findings
