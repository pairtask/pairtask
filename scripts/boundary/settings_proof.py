#!/usr/bin/env python3
"""Run the pinned Claude CLI against shipped permission settings; persist raw evidence only.

Usage: settings_proof.py [OUTPUT_DIR]. PAIRTASK_PROOF_SETTINGS overrides the shipped settings path
and PAIRTASK_PROOF_CLAUDE the claude binary, so the same runner works outside the image.
"""

import http.server
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import threading

from models import SETTINGS_CALLS, Model, settings_events


HERE = Path(__file__).resolve().parent
SETTINGS = os.environ.get("PAIRTASK_PROOF_SETTINGS", "/etc/pairtask/claude-settings.json")
CLAUDE = os.environ.get("PAIRTASK_PROOF_CLAUDE", "claude")
PROMPT = "Execute the deterministic offline permission fixture."
EXPECTED = {"allow": {"send", "acknowledge"}, "none": set(), "deny": {"acknowledge"}}


def case_settings(case, directory):
    if case == "none":
        return None
    settings = json.loads(Path(SETTINGS).read_text())
    if case == "deny":
        settings.setdefault("permissions", {})["deny"] = ["mcp__pairtask__send"]
    path = directory / "settings.json"
    path.write_text(json.dumps(settings, indent=2) + "\n")
    return path


def command(case, directory):
    config = directory / "mcp.json"
    config.write_text(json.dumps({"mcpServers": {"pairtask": {
        "command": sys.executable, "args": [str(HERE / "fake_pairtask.py"), str(directory / "calls.jsonl")]}}}))
    argv = [CLAUDE, "-p", "--strict-mcp-config", "--mcp-config", str(config)]
    settings = case_settings(case, directory)
    if settings is not None:
        argv += ["--settings", str(settings)]
    return argv + ["--permission-mode", "dontAsk", "--setting-sources", "", "--output-format", "stream-json",
                   "--verbose", "--model", "claude-sonnet-4-6", PROMPT]


def environment(directory, port):
    home = directory / "home"
    home.mkdir()
    return {"PATH": os.environ.get("PATH", "/usr/local/bin:/usr/bin:/bin"), "HOME": str(home),
            "CLAUDE_CONFIG_DIR": str(home / "config"), "ANTHROPIC_BASE_URL": f"http://127.0.0.1:{port}",
            "ANTHROPIC_API_KEY": "offline-fixture-not-a-credential", "DISABLE_AUTOUPDATER": "1",
            "DISABLE_NON_ESSENTIAL_MODEL_CALLS": "1"}


def start_model():
    server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Model)
    server.observed = []
    server.events = settings_events
    server.limit = len(SETTINGS_CALLS) + 1
    threading.Thread(target=server.serve_forever, daemon=True).start()
    return server


def execute(argv, directory, env):
    try:
        result = subprocess.run(argv, cwd=str(directory), env=env, capture_output=True, text=True, timeout=60)
        return result.returncode, result.stdout, result.stderr
    except subprocess.TimeoutExpired as error:
        return None, decoded(error.stdout), decoded(error.stderr)


def decoded(data):
    return data.decode(errors="replace") if isinstance(data, bytes) else (data or "")


def version():
    return subprocess.run([CLAUDE, "--version"], capture_output=True, text=True, timeout=30).stdout.strip()


def run_case(case, root):
    directory = root / case
    directory.mkdir(mode=0o700)
    (directory / "calls.jsonl").touch()
    server = start_model()
    try:
        argv = command(case, directory)
        code, stdout, stderr = execute(argv, directory, environment(directory, server.server_port))
    finally:
        server.shutdown()
    (directory / "argv.json").write_text(json.dumps(argv, indent=2) + "\n")
    (directory / "stdout.jsonl").write_text(stdout)
    (directory / "stderr.txt").write_text(stderr)
    (directory / "requests.json").write_text(json.dumps(server.observed, indent=2) + "\n")
    (directory / "run.json").write_text(json.dumps({"exit_code": code, "version": version()}) + "\n")
    return directory, code


def summary(case, directory, code):
    calls = [json.loads(line)["name"] for line in (directory / "calls.jsonl").read_text().splitlines() if line]
    requests = len(json.loads((directory / "requests.json").read_text()))
    print(f"settings proof {case}: exit={code} model_requests={requests} mcp_calls={calls} evidence={directory}",
          flush=True)


def assert_case(directory, case):
    import permission_assertions  # missing verifier fails the proof: never skipped
    permission_assertions.verify_case(directory, EXPECTED[case])


def evidence(case, directory):
    """Print the raw evidence the container's cleanup would lose: argv, MCP
    calls, and the stream's init tools, tool uses, tool results and denials."""
    print(f"BEGIN settings-proof {case} argv", json.dumps(json.loads((directory / "argv.json").read_text())), flush=True)
    for line in (directory / "calls.jsonl").read_text().splitlines():
        print(f"settings-proof {case} mcp-call", line, flush=True)
    for line in (directory / "stdout.jsonl").read_text().splitlines():
        shown = relevant(line)
        if shown:
            print(f"settings-proof {case} stream", json.dumps(shown), flush=True)
    print(f"END settings-proof {case}", flush=True)


def relevant(line):
    """The parts of one stream event that show permissions, never prompts."""
    try:
        event = json.loads(line)
    except ValueError:
        return None
    if event.get("type") == "system":
        return system_event(event)
    if event.get("type") == "result":
        return {"result": event.get("subtype"), "permission_denials": event.get("permission_denials")}
    return tool_blocks(event)


def system_event(event):
    """Init's MCP inventory and mode, and any permission denial notice."""
    if event.get("subtype") == "init":
        return {"init_tools": sorted(t for t in event.get("tools", []) if t.startswith("mcp__")),
                "mcp_servers": event.get("mcp_servers"), "permission_mode": event.get("permissionMode")}
    return event if event.get("subtype") == "permission_denied" else None


def tool_blocks(event):
    """Tool uses and results in an assistant or user message, if any."""
    content = (event.get("message") or {}).get("content")
    if not isinstance(content, list):
        return None
    blocks = [b for b in content if isinstance(b, dict) and b.get("type") in ("tool_use", "tool_result")]
    return {"type": event.get("type"), "blocks": blocks} if blocks else None


def main(root):
    if root.exists():
        shutil.rmtree(root)
    root.mkdir(mode=0o700, parents=True)
    failed = []
    for case in ("allow", "none", "deny"):
        directory, code = run_case(case, root)
        summary(case, directory, code)
        evidence(case, directory)
        if code != 0:
            failed.append(case)
        assert_case(directory, case)
    if failed:
        raise SystemExit(f"settings proof: claude did not exit cleanly for {failed}; evidence in {root}")


if __name__ == "__main__":
    main(Path(sys.argv[1] if len(sys.argv) > 1 else "/var/lib/collab-settings-proof"))
