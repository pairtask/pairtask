"""Restore the local terminal after an interactive session into the sandbox.

A remote TUI (an agent harness, the operator chat) that loses its connection
cannot turn mouse reporting, bracketed paste or the alternate screen off
again; the sequences keep arriving as typed text. The host side can: this
module runs the session and restores the terminal in a finally step, on
normal and non-zero exit and when the wrapper itself is interrupted.
"""

import os
import signal
import subprocess
import sys
import termios

# Mouse reporting (1000, 1002, 1003, SGR 1006), bracketed paste, alternate screen.
RESTORE_SEQUENCE = "\x1b[?1000l\x1b[?1002l\x1b[?1003l\x1b[?1006l\x1b[?2004l\x1b[?1049l"
FORWARDED = (signal.SIGINT, signal.SIGTERM)


def is_tty(stream):
    try:
        return bool(stream.isatty())
    except (AttributeError, ValueError, OSError):
        return False


def fileno(stream):
    try:
        return stream.fileno()
    except (AttributeError, ValueError, OSError):
        return -1


def saved_state(fd):
    """The terminal attributes of fd, or None when it is not a terminal."""
    try:
        if fd < 0 or not os.isatty(fd):
            return None
        return termios.tcgetattr(fd)
    except (OSError, termios.error, ValueError):
        return None


def restore(state, fd, stdout):
    """Put the terminal back: the saved attributes exactly, else `stty sane`
    on a terminal, then the mode resets when stdout is a terminal. Piped,
    scp or tunnel use gets nothing."""
    restore_attributes(state, fd)
    if is_tty(stdout):
        emit(stdout, RESTORE_SEQUENCE)


def restore_attributes(state, fd):
    if state is not None:
        try:
            termios.tcsetattr(fd, termios.TCSADRAIN, state)
        except (OSError, termios.error, ValueError):
            pass
    elif fd >= 0 and os.isatty(fd):
        subprocess.run(["stty", "sane"], stdin=fd, check=False)


def emit(stdout, text):
    """Write to a terminal that may already be closed or broken; the child's
    status still counts."""
    try:
        stdout.write(text)
        stdout.flush()
    except (OSError, ValueError):
        pass


def exit_status(returncode):
    """The shell convention: a child killed by signal N exits 128+N."""
    if returncode < 0:
        return 128 - returncode
    return returncode


def run_session(argv, stdin=None, stdout=None, popen=subprocess.Popen):
    """Run argv on this terminal and restore the terminal afterwards.

    SIGINT and SIGTERM sent to the wrapper are forwarded to the child (one
    arriving before the spawn is forwarded right after it); the wrapper waits
    for the child in every case, restores the previous handlers and the
    terminal, and returns the child's exit status.
    """
    stdin = sys.stdin if stdin is None else stdin
    stdout = sys.stdout if stdout is None else stdout
    fd = fileno(stdin)
    state = saved_state(fd)
    session = {"child": None, "pending": []}

    def forward(signum, _frame):
        child = session["child"]
        if child is None:
            session["pending"].append(signum)
        else:
            deliver(child, signum)

    previous = {}
    try:
        for signum in FORWARDED:
            previous[signum] = signal.signal(signum, forward)
        child = session["child"] = popen(argv)
        for signum in session["pending"]:
            deliver(child, signum)
        return exit_status(wait(child))
    finally:
        for signum, handler in previous.items():
            signal.signal(signum, signal.SIG_DFL if handler is None else handler)
        restore(state, fd, stdout)


def deliver(child, signum):
    try:
        child.send_signal(signum)
    except (ProcessLookupError, OSError):
        pass


def wait(child):
    while True:
        try:
            return child.wait()
        except KeyboardInterrupt:
            deliver(child, signal.SIGINT)
