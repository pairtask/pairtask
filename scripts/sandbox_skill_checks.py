"""Verify the installed skill matches every document in the reviewed image recipe."""

import shlex
import subprocess

from sandbox_source import RECIPE_PATHS


def verify(ssh, manifest):
    prefix = "docs/skills/collab-ai/"
    documents = [name for name in RECIPE_PATHS if name.startswith(prefix)]
    for name in documents:
        installed = "/usr/local/share/pairtask/" + name.removeprefix(prefix)
        command = shlex.join(["sha256sum", installed])
        result = subprocess.run(ssh + [command], check=True, capture_output=True, text=True)
        if result.stdout.split()[0] != manifest["files_sha256"][name]:
            raise ValueError(f"Installed collaboration skill differs from the image recipe: {name}")
    verify_discovery_links(ssh)


def verify_discovery_links(ssh):
    for skill in ("/etc/codex/skills/collab-ai",
                  "/etc/claude-code/.claude/skills/collab-ai"):
        command = shlex.join(["readlink", skill])
        result = subprocess.run(ssh + [command], check=True, capture_output=True, text=True)
        if result.stdout.strip() != "/usr/local/share/pairtask":
            raise ValueError(f"Collaboration skill is not registered for agent discovery: {skill}")
