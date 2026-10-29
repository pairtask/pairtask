"""Runs as agent in an offline, disposable sandbox; never starts a model session."""

import json
import os
from pathlib import Path
import shlex
import sqlite3
import subprocess
import tempfile
import time


HOME = Path.home()
DATABASE = HOME / ".local/share/rtk/history.db"
PROOF = HOME / ".local/state/collab-ai/rtk-proof.json"


def run(command, **kwargs):
    return subprocess.run(command, check=True, capture_output=True, text=True, **kwargs).stdout


def wait_for_setup():
    markers = [HOME / ".local/state/collab-ai" / name for name in ("rtk-initialized", "rtk-pi-initialized")]
    for _ in range(40):
        if all(marker.exists() for marker in markers):
            return
        time.sleep(0.25)
    raise ValueError("RTK setup did not finish; inspect pairtask-rtk-setup.service")


def hook_command(path, agent):
    config = json.loads(path.read_text())
    commands = [hook["command"] for entry in config["hooks"]["PreToolUse"]
                for hook in entry["hooks"] if f"rtk hook {agent}" in hook.get("command", "")]
    if len(commands) != 1:
        raise ValueError(f"Expected one installed {agent} RTK hook")
    return shlex.split(commands[0])


def check_hooks():
    payload = {"hook_event_name": "PreToolUse", "tool_name": "Bash", "permission_mode": "default",
               "cwd": str(HOME), "tool_input": {"command": "git status"}}
    for agent, relative in (("codex", ".codex/hooks.json"), ("claude", ".claude/settings.json")):
        command = hook_command(HOME / relative, agent)
        result = json.loads(run(command, input=json.dumps(payload)))
        assert result["hookSpecificOutput"]["updatedInput"]["command"] == "rtk git status"
        assert run(command, input="not-json") == ""  # Malformed events leave commands alone.
    assert "RTK.md" in (HOME / ".codex/AGENTS.md").read_text()


def check_pi():
    """Pi loads the extension from its own directory; it rewrites through rtk."""
    extension = HOME / ".pi/agent/extensions/rtk.ts"
    assert extension.is_file() and not extension.is_symlink(), "RTK's Pi extension is missing"
    assert extension.stat().st_uid == os.getuid()
    assert "rtk rewrite" in extension.read_text()
    rewritten = subprocess.run(["rtk", "rewrite", "git status"], capture_output=True, text=True)
    assert rewritten.stdout.strip() == "rtk git status", rewritten


def history_count():
    assert DATABASE.is_file() and DATABASE.stat().st_uid == os.getuid()
    with sqlite3.connect(f"file:{DATABASE}?mode=ro", uri=True) as db:
        assert db.execute("PRAGMA integrity_check").fetchone()[0] == "ok"
        return db.execute("SELECT count(*) FROM commands").fetchone()[0]


def check_tracking():
    if PROOF.exists():
        assert history_count() >= json.loads(PROOF.read_text())["commands"]
    with tempfile.TemporaryDirectory() as repository:
        run(["git", "init", "-q", repository])
        run(["rtk", "git", "status"], cwd=repository)
    failed = subprocess.run(["rtk", "proxy", "sh", "-c", "exit 7"], capture_output=True)
    assert failed.returncode == 7
    json.loads(run(["rtk", "gain", "--all", "--format", "json"]))
    count = history_count()
    assert count > 0
    PROOF.write_text(json.dumps({"commands": count}))


wait_for_setup()
check_hooks()
check_pi()
check_tracking()
print("PASS: RTK hook rewriting for Codex, Claude and Pi, exit status, private history and retained usage.")
