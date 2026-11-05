"""The Incus server version pairtask requires (#312), and the check against
the server a host script drives: its /1.0 server_version, never the
client's. One source for the requirement, so the docs and the check agree.
"""

import json
import re
import subprocess
from typing import Optional, Tuple

Version = Tuple[int, int, int]

# QUERY_TIMEOUT bounds the /1.0 query, in seconds.
QUERY_TIMEOUT = 30

# Supported servers, as half-open ranges: 7.0.x from 7.0.2 (LTS), and 7.5 on.
SUPPORTED = (((7, 0, 2), (7, 1, 0)), ((7, 5, 0), None))
REQUIREMENT = "7.0.x at 7.0.2 or later, or 7.5 or later"


class IncusVersionError(ValueError):
    """The server's version is unsupported, or couldn't be verified."""


def parse_version(text: object) -> Optional[Version]:
    """MAJOR.MINOR or MAJOR.MINOR.PATCH as Incus reports it; None otherwise."""
    if not isinstance(text, str):
        return None
    match = re.fullmatch(r"([0-9]+)\.([0-9]+)(?:\.([0-9]+))?", text)
    if match is None:
        return None
    return int(match.group(1)), int(match.group(2)), int(match.group(3) or 0)


def supported(version: Version) -> bool:
    return any(low <= version and (high is None or version < high) for low, high in SUPPORTED)


def server_version(metadata: object) -> Optional[str]:
    """The server_version of an `incus query REMOTE:/1.0` answer, if it has one."""
    if not isinstance(metadata, dict):
        return None
    environment = metadata.get("environment")
    if not isinstance(environment, dict):
        return None
    found = environment.get("server_version")
    return found if isinstance(found, str) else None


def track(version: Version) -> str:
    """Where an unsupported server goes: forward on its own track."""
    if version < (7, 0, 0):
        return "move to the 7.0 LTS track at 7.0.2 or later"
    if version < (7, 1, 0):
        return "upgrade within 7.0 LTS to 7.0.2 or later"
    return "upgrade forward to 7.5 or later; never back to 7.0, since Incus can't downgrade its database"


def guidance(remote: str) -> str:
    """How to upgrade the server behind remote, by where it runs."""
    if remote == "local":
        return ("Upgrade the host's Incus packages from the repository you installed them from; "
                "pairtask doesn't manage this host's Incus. See docs/sandbox.md, Incus server version.")
    return ("The Colima VM holds its Incus packages, so `apt upgrade` and `brew upgrade incus` won't move the server: "
            "upgrade the held incus packages inside the VM deliberately, after stopping the sandbox and backing up "
            "Incus. See docs/sandbox.md, Incus server version.")


def check(metadata: object, remote: str) -> str:
    """The server's version when pairtask supports it; IncusVersionError
    naming what was found, what is required and how to get there otherwise."""
    found = server_version(metadata)
    version = parse_version(found)
    if version is None:
        reason = "no server_version in its /1.0 answer" if found is None else f"unrecognized server_version {found!r}"
        raise IncusVersionError(f"Incus server version on {remote} is unverified ({reason}); "
                                f"pairtask requires {REQUIREMENT}.")
    if not supported(version):
        raise IncusVersionError(f"Incus server {found} on {remote} is unsupported: pairtask requires {REQUIREMENT}. "
                                f"To fix: {track(version)}. {guidance(remote)}")
    return found


def query_server(remote: str, run=None) -> object:
    """The server's /1.0 answer, or IncusVersionError when it can't be read."""
    try:
        result = (run or subprocess.run)(["incus", "query", f"{remote}:/1.0"], capture_output=True, text=True,
                     check=True, timeout=QUERY_TIMEOUT)
        return json.loads(result.stdout)
    except (subprocess.SubprocessError, OSError, ValueError) as err:
        raise IncusVersionError(f"Incus server version on {remote} is unverified (its /1.0 query failed: "
                                f"{type(err).__name__}); pairtask requires {REQUIREMENT}.") from err
