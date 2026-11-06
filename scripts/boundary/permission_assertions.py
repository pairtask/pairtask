"""Assertions on real Claude dontAsk permission proof artifacts, never prose."""

import json
from itertools import chain
from pathlib import Path

PREFIX = "mcp__pairtask__"
ARGUMENTS = {
    "send": {"to": "operator", "text": "permission fixture", "message_id": "permission-send"},
    "acknowledge": {"message_id": "permission-received"},
    "publish_review": {"review_id": "permission-review"},
}
ALLOWED_TOOLS = {
    "send", "acknowledge", "receive", "wait", "wait_reply", "listen", "listener_status",
    "status", "read_status", "claim", "finish_claim", "release_claim", "read_claims",
    "join_room", "leave_room", "list_rooms", "workspaces",
}


def require(condition, message):
    if not condition:
        raise ValueError(message)


def json_lines(path):
    return [json.loads(line) for line in path.read_text().splitlines() if line.strip()]


def option(argv, name):
    require(argv.count(name) == 1, f"expected exactly one {name}: {argv}")
    index = argv.index(name)
    require(index + 1 < len(argv), f"missing value for {name}")
    return argv[index + 1]


def verify_argv(argv, has_settings):
    require(isinstance(argv, list) and all(isinstance(v, str) for v in argv), "argv must be a string array")
    require(option(argv, "--permission-mode") == "dontAsk", "proof must exercise dontAsk")
    require(option(argv, "--setting-sources") == "", "user/project settings must be excluded")
    require("--strict-mcp-config" in argv, "proof must use only its isolated MCP configuration")
    option(argv, "--mcp-config")
    forbidden = {"--allowedtools", "--allowed-tools", "--disallowedtools", "--disallowed-tools",
                 "--dangerously-skip-permissions", "--allow-dangerously-skip-permissions"}
    require(not any(v.split("=", 1)[0].lower() in forbidden for v in argv), "permission flags confound the proof")
    require(any(v.split("=", 1)[0] == "--settings" for v in argv) == has_settings, "settings/control argv mismatch")
    if has_settings:
        option(argv, "--settings")


def verify_settings(directory, allowed):
    path = directory / "settings.json"
    if not allowed:
        require(not path.exists(), "omitted-settings control must not load a settings snapshot")
        return
    permissions = json.loads(path.read_text())["permissions"]
    names = permissions["allow"]
    require(len(names) == len(ALLOWED_TOOLS) and set(names) == {PREFIX + name for name in ALLOWED_TOOLS},
            "proof must load the shipped explicit allowlist")
    expected_deny = [PREFIX + "send"] if allowed == {"acknowledge"} else []
    require(permissions.get("deny", []) == expected_deny, "deny control must preserve allow and explicitly deny send")


def message_blocks(event, message_type, block_type):
    if event.get("type") != message_type:
        return []
    return [block for block in event.get("message", {}).get("content", []) if block.get("type") == block_type]


def stream_blocks(events, message_type, block_type):
    return chain.from_iterable(message_blocks(event, message_type, block_type) for event in events)


def verify_requests(requests):
    require(set(requests) == {PREFIX + name for name in ARGUMENTS}, "all three tools must really be requested")
    require(len({block["id"] for block in requests.values()}) == 3, "tool request IDs must be distinct")
    for name, arguments in ARGUMENTS.items():
        require(requests[PREFIX + name].get("input") == arguments, f"wrong model arguments for {name}")


def requested_tools(events):
    requests = {}
    for block in stream_blocks(events, "assistant", "tool_use"):
        name = block["name"]
        require(name not in requests, f"duplicate model tool request: {name}")
        requests[name] = block
    verify_requests(requests)
    return requests


def verify_calls(calls, allowed):
    require(len(calls) == len(allowed), "unexpected or missing actual MCP tool calls")
    require({call["name"] for call in calls} == allowed, "actual MCP calls disagree with the permission case")
    for call in calls:
        require(call.get("arguments") == ARGUMENTS[call["name"]], f"wrong executed arguments: {call}")


def verify_catalog(events, allowed, version):
    initial = [e for e in events if e.get("type") == "system" and e.get("subtype") == "init"]
    require(len(initial) == 1, "actual CLI tool catalog must be recorded")
    require(initial[0].get("permissionMode") == "dontAsk", "actual native runtime must report dontAsk")
    require(initial[0].get("claude_code_version") == version.split()[0], "native initialization and recorded version must agree")
    require(any(server.get("name") == "pairtask" and server.get("status") == "connected"
                for server in initial[0].get("mcp_servers", [])), "fixture MCP server must actually connect")
    present = {name for name in initial[0].get("tools", []) if name.startswith(PREFIX)}
    omitted = {"send"} if allowed == {"acknowledge"} else set()
    require(present == {PREFIX + name for name in ARGUMENTS if name not in omitted},
            "explicit deny must remove send, while other fixture tools remain discoverable")


def verify_denied_send(events, requests, allowed):
    if allowed != {"acknowledge"}:
        return
    identity = requests[PREFIX + "send"]["id"]
    results = [block for block in stream_blocks(events, "user", "tool_result") if block.get("tool_use_id") == identity]
    require(len(results) == 1 and results[0].get("is_error") is True,
            "denied send must have an ID-correlated native tool error")
    content = results[0].get("content", "")
    require(isinstance(content, str) and "No such tool available: " + PREFIX + "send" in content,
            "native error must identify the send tool removed by the deny rule")


def verify_denials(events, requests, allowed):
    results = [event for event in events if event.get("type") == "result"]
    require(len(results) == 1, "expected the real CLI's single final result")
    require(not results[0].get("is_error", False), "the real CLI must finish the proof successfully")
    denials = results[0].get("permission_denials", [])
    omitted = {"send"} if allowed == {"acknowledge"} else set()
    expected = {PREFIX + name for name in ARGUMENTS if name not in allowed | omitted}
    require(len(denials) == len(expected) and {d["tool_name"] for d in denials} == expected,
            "missing or unexpected structured runtime permission denials")
    for denial in denials:
        requested = requests[denial["tool_name"]]
        require(denial.get("tool_use_id") == requested["id"], "denial must identify the actual model tool request")


def verify_case(directory, expected_allowed):
    directory, allowed = Path(directory), set(expected_allowed)
    require(allowed in ({"send", "acknowledge"}, set(), {"acknowledge"}), "unknown permission proof case")
    run = json.loads((directory / "run.json").read_text())
    require(run.get("exit_code") == 0, "real Claude did not finish successfully")
    version = run.get("version", run.get("claude_version"))
    require(isinstance(version, str) and version.strip(), "actual Claude version must be recorded")
    verify_argv(json.loads((directory / "argv.json").read_text()), bool(allowed))
    verify_settings(directory, allowed)
    events = json_lines(directory / "stdout.jsonl")
    requests = requested_tools(events)
    require(len(json.loads((directory / "requests.json").read_text())) == 4,
            "three actual provider tool turns and a completion turn are required")
    verify_catalog(events, allowed, version)
    verify_calls(json_lines(directory / "calls.jsonl"), allowed)
    verify_denials(events, requests, allowed)
    verify_denied_send(events, requests, allowed)
    return {"allowed": sorted(allowed), "denied": sorted(set(ARGUMENTS) - allowed), "claude_version": version}
