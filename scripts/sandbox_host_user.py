"""The host commands run as the operator's normal user, never as root: the
host checkout and the pairtask state under their home belong to that user,
and a command run under sudo would leave root-owned files behind and manage
the deployment from root's point of view. A refusal happens before any
planning or state read; it is a one-line answer, not an exception."""
import os
import sys

MESSAGE = ("%s: run as your normal user, not root or sudo; the host checkout and your pairtask state "
           "belong to that user")


def effective_uid():
    return os.geteuid()


def running_as_root(environ=None):
    """Root, or any user reached through sudo (SUDO_USER is set either way)."""
    environ = os.environ if environ is None else environ
    return effective_uid() == 0 or bool(environ.get("SUDO_USER"))


def refuse_root(prog, environ=None, stream=None):
    """1 with the message on stderr when running as root or under sudo, else 0."""
    if not running_as_root(environ):
        return 0
    print(MESSAGE % prog, file=sys.stderr if stream is None else stream)
    return 1
