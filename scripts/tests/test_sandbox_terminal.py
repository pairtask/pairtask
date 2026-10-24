"""Exercise host cleanup through real PTYs, including wrapper-only signals."""

import io
import os
from pathlib import Path
import pty
import re
import runpy
import select
import signal
import subprocess
import sys
import termios
import time
import unittest
from unittest import mock

SCRIPTS = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(SCRIPTS))
import sandbox_terminal as terminal

RESETS = [b"\x1b[?%dl" % mode for mode in (1000, 1002, 1003, 1006, 2004, 1049)]
LAUNCH = "from sandbox_terminal import run_session; import sys; sys.exit(run_session(sys.argv[1:]))"
DIRTY = """import os, signal, sys, termios, tty
assert signal.getsignal(signal.SIGINT) != signal.SIG_IGN
tty.setraw(0)
"""


def terminal_settings(fd):
    attrs = termios.tcgetattr(fd)
    if sys.platform == "darwin":
        # XNU sets PENDIN when restoring ICANON: transient input state, not
        # a setting. bsd/kern/tty.c in apple-oss-distributions/xnu, TIOCSETA.
        attrs[3] &= ~termios.PENDIN
    return attrs


class SessionTerminal:
    def __init__(self):
        self.master, self.slave = pty.openpty()
        attrs = termios.tcgetattr(self.slave)
        attrs[3] &= ~termios.ECHO
        attrs[6][termios.VERASE] = b"\x08"
        termios.tcsetattr(self.slave, termios.TCSANOW, attrs)
        self.original = terminal_settings(self.slave)
        self.process = None
        self.child_pid = None
        self.output = b""

    def __enter__(self):
        return self

    def __exit__(self, kind, _error, _traceback):
        if kind is not None and self.child_pid is not None:
            try:
                os.kill(self.child_pid, signal.SIGKILL)
            except ProcessLookupError:
                pass
        if self.process is not None and self.process.poll() is None:
            self.process.kill()
            self.process.wait(timeout=5)
        os.close(self.master)
        os.close(self.slave)

    def start(self, source, piped=False):
        env = dict(os.environ, PYTHONPATH=str(SCRIPTS))
        self.process = subprocess.Popen(
            [sys.executable, "-c", LAUNCH, sys.executable, "-c", source],
            stdin=self.slave, stdout=subprocess.PIPE if piped else self.slave,
            stderr=subprocess.PIPE, env=env,
        )
        return self.process

    def read_ready(self):
        until = time.monotonic() + 5
        while time.monotonic() < until:
            self.read_output(0.05)
            found = re.search(rb"READY ([0-9]+)\n", self.output)
            if found:
                self.child_pid = int(found.group(1))
                return
            if self.process.poll() is not None:
                _, error = self.process.communicate()
                raise AssertionError("session exited before ready: %r" % error)
        raise AssertionError("session never became ready")

    def read_output(self, timeout):
        ready, _, _ = select.select([self.master], [], [], timeout)
        if not ready:
            return False
        self.output += os.read(self.master, 4096)
        return True

    def wait_exit(self):
        # Act as the terminal emulator while the wrapper runs. On macOS,
        # TCSADRAIN cannot finish until the master consumes queued output.
        until = time.monotonic() + 8
        while time.monotonic() < until:
            self.read_output(0.05)
            if self.process.poll() is not None:
                return
        raise AssertionError("session did not exit while its terminal was drained")

    def finish(self):
        self.wait_exit()
        output, error = self.process.communicate(timeout=8)
        while self.read_output(0):
            pass
        return self.output if output is None else output, error


class TerminalRestoreAcceptanceTests(unittest.TestCase):
    def assert_restored(self, session, output):
        self.assertEqual(terminal_settings(session.slave), session.original)
        for reset in RESETS:
            self.assertIn(reset, output)
        self.assertGreater(output.rfind(b"\x1b[?1003l"), output.find(b"\x1b[?1003h"))

    def test_normal_and_nonzero_exit_restore_exact_saved_terminal(self):
        for code in (0, 23):
            with self.subTest(code=code), SessionTerminal() as session:
                child = DIRTY + "os.write(1, b'\\x1b[?1003h'); sys.exit(%d)" % code
                process = session.start(child)
                output, error = session.finish()
                self.assertEqual(process.returncode, code, error)
                self.assert_restored(session, output)

    def test_piped_stdout_has_no_cleanup_bytes_and_keeps_exit_code(self):
        with SessionTerminal() as session:
            process = session.start(DIRTY + "os.write(1, b'payload'); sys.exit(23)", piped=True)
            output, error = session.finish()
            self.assertEqual(process.returncode, 23, error)
            self.assertEqual(output, b"payload")
            self.assertEqual(terminal_settings(session.slave), session.original)

    def test_wrapper_only_interrupt_restores_and_reaps_child(self):
        for signum in (signal.SIGINT, signal.SIGTERM):
            with self.subTest(signal=signum), SessionTerminal() as session:
                child = DIRTY + "os.write(1, b'\\x1b[?1003hREADY %d\\n' % os.getpid()); signal.pause()"
                process = session.start(child)
                session.read_ready()
                os.kill(process.pid, signum)
                output, error = session.finish()
                status = process.returncode if process.returncode >= 0 else 128 - process.returncode
                self.assertEqual(status, 128 + signum, error)
                self.assert_restored(session, output)
                with self.assertRaises(ProcessLookupError):
                    os.kill(session.child_pid, 0)

    def test_child_signal_exit_is_preserved(self):
        with SessionTerminal() as session:
            process = session.start(DIRTY + "os.kill(os.getpid(), signal.SIGTERM)")
            output, error = session.finish()
            self.assertEqual(process.returncode, 128 + signal.SIGTERM, error)
            self.assert_restored(session, output)

    def test_spawn_failure_still_restores_terminal_and_handlers(self):
        previous = {sig: signal.getsignal(sig) for sig in (signal.SIGINT, signal.SIGTERM)}
        sink = mock.Mock()
        sink.isatty.return_value = True
        with mock.patch.object(terminal, "saved_state", return_value=None):
            with self.assertRaises(FileNotFoundError):
                terminal.run_session(["missing"], stdin=io.StringIO(), stdout=sink,
                                     popen=mock.Mock(side_effect=FileNotFoundError("missing")))
        self.assertEqual({sig: signal.getsignal(sig) for sig in previous}, previous)
        sink.write.assert_called_once_with(terminal.RESTORE_SEQUENCE)
        sink.flush.assert_called_once()

    def test_signal_during_spawn_is_delivered_after_child_exists(self):
        child = mock.Mock()
        child.wait.return_value = -signal.SIGTERM

        def spawn(_argv):
            signal.raise_signal(signal.SIGTERM)
            return child

        result = terminal.run_session(["fake"], stdin=io.StringIO(), stdout=io.StringIO(), popen=spawn)
        self.assertEqual(result, 128 + signal.SIGTERM)
        child.send_signal.assert_called_once_with(signal.SIGTERM)
        child.wait.assert_called_once()

    def test_cleanup_output_failure_does_not_replace_session_status(self):
        child = mock.Mock()
        child.wait.return_value = 23
        output = mock.Mock()
        output.isatty.return_value = True
        output.write.side_effect = BrokenPipeError("terminal disappeared")
        result = terminal.run_session(["fake"], stdin=io.StringIO(), stdout=output,
                                      popen=mock.Mock(return_value=child))
        self.assertEqual(result, 23)


class SessionEntryPointAcceptanceTests(unittest.TestCase):
    def setUp(self):
        self.main = runpy.run_path(str(SCRIPTS / "sandbox-session.py"))["main"]
        self.run = mock.Mock(return_value=23)
        self.patch = mock.patch.dict(self.main.__globals__, {"run_session": self.run})
        self.patch.start()
        self.addCleanup(self.patch.stop)

    def test_shell_uses_existing_config_and_preserves_arguments(self):
        with mock.patch("sys.stdin.isatty", return_value=True):
            result = self.main(["shell", "--ssh-config", "/tmp/a config", "printf", "literal;$(no)"])
        self.assertEqual(result, 23)
        self.run.assert_called_once_with(["ssh", "-F", "/tmp/a config", "workspace", "printf", "literal;$(no)"])

    def test_shell_allocates_a_remote_tty_only_when_requested(self):
        result = self.main(["shell", "--tty", "--config", "/tmp/a config", "pairtask", "dashboard"])
        self.assertEqual(result, 23)
        self.run.assert_called_once_with([
            "ssh", "-t", "-F", "/tmp/a config", "workspace", "pairtask", "dashboard",
        ])

    def test_chat_wraps_current_secured_entry_point(self):
        result = self.main(["chat", "--remote", "lab", "--project", "work", "--secured", "secured-one"])
        self.assertEqual(result, 23)
        self.run.assert_called_once_with([
            "incus", "--project", "work", "exec", "-t", "lab:secured-one", "--", "pairtask", "operator", "chat",
        ])

    def test_invalid_target_is_rejected_before_launch(self):
        with self.assertRaises(SystemExit), mock.patch("sys.stderr", io.StringIO()):
            self.main(["chat", "--remote", "bad:target"])
        self.run.assert_not_called()
