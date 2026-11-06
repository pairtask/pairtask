"""The native permission proof must reject missing observations and prose claims."""

import importlib.util
import json
from pathlib import Path
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[2]
SPEC = importlib.util.spec_from_file_location("permission_assertions", ROOT / "scripts/boundary/permission_assertions.py")
PROOF = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(PROOF)


def records(allowed):
    argv = ["claude", "-p", "--strict-mcp-config", "--mcp-config", "fixture-mcp.json",
            "--permission-mode", "dontAsk", "--setting-sources", ""]
    requests = [{"type": "tool_use", "id": "call-" + name, "name": PROOF.PREFIX + name,
                 "input": dict(arguments)} for name, arguments in PROOF.ARGUMENTS.items()]
    omitted = {"send"} if allowed == {"acknowledge"} else set()
    denials = [{"tool_name": PROOF.PREFIX + name, "tool_use_id": "call-" + name}
               for name in PROOF.ARGUMENTS if name not in allowed | omitted]
    data = {"argv.json": argv, "run.json": {"exit_code": 0, "version": "2.1.283"},
            "requests.json": [{}, {}, {}, {}],
            "stdout.jsonl": [{"type": "system", "subtype": "init", "permissionMode": "dontAsk", "claude_code_version": "2.1.283",
                              "mcp_servers": [{"name": "pairtask", "status": "connected"}],
                              "tools": [PROOF.PREFIX + name for name in PROOF.ARGUMENTS if name not in omitted]},
                             {"type": "assistant", "message": {"content": requests}},
                             {"type": "result", "permission_denials": denials}],
            "calls.jsonl": [{"name": name, "arguments": dict(PROOF.ARGUMENTS[name])} for name in sorted(allowed)]}
    if allowed:
        argv.extend(["--settings", "/etc/pairtask/claude-settings.json"])
        permissions = {"allow": [PROOF.PREFIX + name for name in sorted(PROOF.ALLOWED_TOOLS)]}
        if allowed == {"acknowledge"}:
            permissions["deny"] = [PROOF.PREFIX + "send"]
            error = {"type": "tool_result", "tool_use_id": "call-send", "is_error": True,
                     "content": "<tool_use_error>No such tool available: mcp__pairtask__send</tool_use_error>"}
            data["stdout.jsonl"].insert(-1, {"type": "user", "message": {"content": [error]}})
        data["settings.json"] = {"permissions": permissions}
    return data


def write_records(directory, data):
    for name, value in data.items():
        text = "\n".join(json.dumps(item) for item in value) if name.endswith(".jsonl") else json.dumps(value)
        (directory / name).write_text(text)


class ClaudePermissionProofAssertions(unittest.TestCase):
    def verify(self, data, allowed):
        with tempfile.TemporaryDirectory() as temporary:
            directory = Path(temporary)
            write_records(directory, data)
            return PROOF.verify_case(directory, allowed)

    def test_all_three_cases_require_observed_calls_requests_and_denials(self):
        for allowed in ({"send", "acknowledge"}, set(), {"acknowledge"}):
            with self.subTest(allowed=allowed):
                self.assertEqual(self.verify(records(allowed), allowed)["allowed"], sorted(allowed))

    def test_prose_missing_calls_or_missing_tool_requests_cannot_pass(self):
        for missing in ("calls", "requests", "denials"):
            with self.subTest(missing=missing):
                allowed = {"send", "acknowledge"}
                data = records(allowed)
                if missing == "calls":
                    data["calls.jsonl"] = []
                elif missing == "requests":
                    data["stdout.jsonl"][1]["message"]["content"] = [{"type": "text", "text": "all checks passed"}]
                else:
                    data["stdout.jsonl"][-1]["permission_denials"] = []
                with self.assertRaises(ValueError):
                    self.verify(data, allowed)

    def test_excluded_calls_wrong_arguments_and_uncorrelated_denials_are_rejected(self):
        for flaw in ("excluded", "arguments", "denial"):
            with self.subTest(flaw=flaw):
                allowed = {"send", "acknowledge"}
                data = records(allowed)
                if flaw == "excluded":
                    data["calls.jsonl"].append({"name": "publish_review", "arguments": PROOF.ARGUMENTS["publish_review"]})
                elif flaw == "arguments":
                    data["calls.jsonl"][0]["arguments"] = {"message_id": "not-the-requested-message"}
                else:
                    data["stdout.jsonl"][-1]["permission_denials"][0]["tool_use_id"] = "not-the-actual-request"
                with self.assertRaises(ValueError):
                    self.verify(data, allowed)

    def test_cli_allow_flags_or_wrong_permission_mode_cannot_substitute_for_settings(self):
        for flag in ("--allowedTools", "--disallowedTools", "--dangerously-skip-permissions"):
            with self.subTest(flag=flag):
                allowed = {"send", "acknowledge"}
                data = records(allowed)
                data["argv.json"].append(flag)
                with self.assertRaises(ValueError):
                    self.verify(data, allowed)

    def test_deny_control_requires_both_allow_and_deny_for_send(self):
        allowed = {"acknowledge"}
        data = records(allowed)
        data["settings.json"]["permissions"].pop("deny")
        with self.assertRaises(ValueError):
            self.verify(data, allowed)

    def test_deny_control_needs_catalog_absence_and_the_correlated_native_error(self):
        for missing in ("catalog", "error", "correlation"):
            with self.subTest(missing=missing):
                allowed = {"acknowledge"}
                data = records(allowed)
                if missing == "catalog":
                    data["stdout.jsonl"][0]["tools"].append(PROOF.PREFIX + "send")
                elif missing == "error":
                    data["stdout.jsonl"] = [e for e in data["stdout.jsonl"] if e["type"] != "user"]
                else:
                    data["stdout.jsonl"][-2]["message"]["content"][0]["tool_use_id"] = "wrong-call"
                with self.assertRaises(ValueError):
                    self.verify(data, allowed)

    def test_failed_process_or_incomplete_provider_exchange_cannot_pass(self):
        for missing in ("process", "provider"):
            with self.subTest(missing=missing):
                allowed = {"send", "acknowledge"}
                data = records(allowed)
                if missing == "process":
                    data["run.json"]["exit_code"] = 1
                else:
                    data["requests.json"].pop()
                with self.assertRaises(ValueError):
                    self.verify(data, allowed)

    def test_wrong_mode_unconnected_server_or_unexpected_settings_are_rejected(self):
        for flaw in ("argv-mode", "runtime-mode", "version", "connection", "control-settings"):
            with self.subTest(flaw=flaw):
                allowed = set() if flaw == "control-settings" else {"send", "acknowledge"}
                data = records(allowed)
                if flaw == "argv-mode":
                    data["argv.json"][data["argv.json"].index("dontAsk")] = "bypassPermissions"
                elif flaw == "runtime-mode":
                    data["stdout.jsonl"][0]["permissionMode"] = "bypassPermissions"
                elif flaw == "version":
                    data["stdout.jsonl"][0]["claude_code_version"] = "not-the-recorded-version"
                elif flaw == "connection":
                    data["stdout.jsonl"][0]["mcp_servers"][0]["status"] = "failed"
                else:
                    data["argv.json"].append("--settings=/tmp/not-omitted.json")
                with self.assertRaises(ValueError):
                    self.verify(data, allowed)


if __name__ == "__main__":
    unittest.main()
