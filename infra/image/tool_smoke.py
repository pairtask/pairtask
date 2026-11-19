"""Starting an agent client to see that it works: the checks shared by the
tools lane (CI, on a runner) and an in-place update (inside a container,
as an unprivileged user).

A client proves itself by naming its version as a whole token, and Codex
also by answering an initialize request on its App Server with the config
it will run with. Reads from a client are bounded in time and in bytes.
"""

import json
import os
from pathlib import Path
import re
import select
import shutil
import subprocess
import time

RUN_TIMEOUT = 60


class SmokeError(ValueError):
    """One finding about one client."""


def reports(version, text):
    """text names version as a whole token, not as the start of a longer one."""
    return bool(version) and re.search(r"(?<![\w.])" + re.escape(version) + r"(?![\w.])", text) is not None


def client_environment(home, **extra):
    """A private home and PATH, and nothing else of the caller's."""
    environment = {"HOME": str(home), "PATH": os.environ.get("PATH", "/usr/bin:/bin")}
    environment.update(extra)
    return environment


class Handshake:
    """How a handshake is run: its time limit, the client name it gives,
    and, when the caller is root, a command prefix that drops privileges
    to owner, who is then given the CODEX_HOME."""

    def __init__(self, timeout=RUN_TIMEOUT, client="pairtask-tools-lane", prefix=(), owner=None):
        self.timeout, self.client, self.prefix, self.owner = timeout, client, tuple(prefix), owner


def codex_handshake(binary, home, config, how=None):
    """Start the App Server as pairtask-codex does and complete initialize
    with config in a private CODEX_HOME; the answer's codexHome shows that
    config was the one read."""
    how = how or Handshake()
    timeout, client, prefix, owner = how.timeout, how.client, how.prefix, how.owner
    codex_home = Path(home) / "codex-home"
    codex_home.mkdir(exist_ok=True)
    shutil.copyfile(config, codex_home / "config.toml")
    if owner:
        # The caller is root and the client runs as owner, which writes here.
        shutil.chown(codex_home, owner)
        shutil.chown(codex_home / "config.toml", owner)
    environment = client_environment(home, CODEX_HOME=str(codex_home))
    request = {"jsonrpc": "2.0", "id": 1, "method": "initialize",
               "params": {"clientInfo": {"name": client, "version": "1"}, "capabilities": {"experimentalApi": True}}}
    with subprocess.Popen([*prefix, str(binary), "app-server", "--listen", "stdio://"], stdin=subprocess.PIPE, stdout=subprocess.PIPE,
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
        raise SmokeError("the App Server didn't accept initialize")
    if answer["result"].get("codexHome") != str(codex_home):
        raise SmokeError("the App Server didn't read the image's config")
    return answer["result"]


def accepted(answer):
    """The answer to request 1 carries a result and no error."""
    return "error" not in answer and isinstance(answer.get("result"), dict)


def first_answer(process, timeout):
    """The App Server's answer to request 1, skipping notifications it sends
    first. Reads never block past the deadline, a partial line included, and
    everything read until the answer is bounded by MAX_PREAMBLE."""
    reader = AnswerReader(process.stdout, time.monotonic() + timeout)
    while True:
        frame = json.loads(reader.line())
        if frame.get("id") == 1:
            return frame
        if time.monotonic() >= reader.deadline:
            raise SmokeError(NO_ANSWER_IN_TIME)


# How many bytes the App Server may send, in total, up to and including its
# answer to initialize.
MAX_PREAMBLE = 1 << 20
TOO_MUCH_PREAMBLE = "the App Server sent over {} bytes before answering initialize".format(MAX_PREAMBLE)
NO_ANSWER_IN_TIME = "the App Server didn't answer initialize in time"


class AnswerReader:
    """Lines from the App Server, bounded in time by a deadline and in size
    by MAX_PREAMBLE over every byte read."""

    def __init__(self, stream, deadline):
        self.stream, self.deadline = stream, deadline
        self.buffered, self.read = b"", 0

    def line(self):
        while b"\n" not in self.buffered:
            self.fill()
        line, _, self.buffered = self.buffered.partition(b"\n")
        return line.decode("utf-8", "replace")

    def fill(self):
        """One read, once the stream is ready before the deadline."""
        ready, _, _ = select.select([self.stream], [], [], max(self.deadline - time.monotonic(), 0))
        if not ready or time.monotonic() >= self.deadline:
            raise SmokeError(NO_ANSWER_IN_TIME)
        chunk = os.read(self.stream.fileno(), 1 << 16)
        if not chunk:
            raise SmokeError("the App Server closed without answering initialize")
        self.read += len(chunk)
        if self.read > MAX_PREAMBLE:
            raise SmokeError(TOO_MUCH_PREAMBLE)
        self.buffered += chunk
