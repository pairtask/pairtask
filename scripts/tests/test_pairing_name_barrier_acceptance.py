"""Host name barrier refuses uncertain invalidation and incomplete release."""

import hashlib
import json
from pathlib import Path
import sys
from types import SimpleNamespace
import unittest
from urllib.parse import parse_qs, urlsplit

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import sandbox_pairing_barrier as barrier


class IncusFixture:
    def __init__(self):
        self.state, self.marker = "Stopped", True
        self.name = '  "quoted" \\ $cash % #name;  '
        self.data = json.dumps({"name": self.name}).encode()
        self.calls, self.uploads = [], []
        self.fail = None

    def run(self, command, **kwargs):
        args = self.arguments(command)
        self.calls.append((args, kwargs))
        handler = getattr(self, "response_" + args[0])
        key, output = handler(args, kwargs)
        if key == self.fail:
            return SimpleNamespace(returncode=1, stdout=b"", stderr=b"permission or transport fixture failure")
        return SimpleNamespace(returncode=0, stdout=output, stderr=b"")

    def arguments(self, command):
        if command[1] == "query":
            if "--project" in command:
                raise AssertionError("incus query accepts project only in its URL")
            return command[1:]
        args = command[3:]
        if args[0] == "query":
            raise AssertionError("incus --project P query is refused by real Incus")
        return args

    def response_list(self, _args, _kwargs):
        instances = [] if self.state is None else [{"name": "secured", "status": self.state}]
        return "list", json.dumps(instances).encode()

    def response_query(self, args, _kwargs):
        parameters = parse_qs(urlsplit(args[-1]).query)
        if parameters.get("project") != ["fixture"]:
            raise AssertionError("query URL lost its project")
        if args[2] == "DELETE":
            self.marker = False
            return "query-DELETE", b""
        names = self.directory(parameters["path"][0])
        return "query-GET", json.dumps(names).encode()

    def directory(self, path):
        if path == "/etc":
            return ["pairtask"]
        return [barrier.READY] if self.marker else []

    def response_file(self, args, kwargs):
        if args[1] == "pull":
            return "file-pull", self.data
        self.uploads.append((args, kwargs["input"]))
        return "file-push", b""

    def response_exec(self, args, _kwargs):
        guest = args[args.index("--") + 1:]
        if guest[0] == barrier.SETUP:
            return "publish", b""
        if guest[:2] == ["systemctl", "restart"]:
            return "restart", b""
        if guest[0] == "pairtask":
            return "status", json.dumps({"pairing_result": {"self_name": self.name or "workspace"}}).encode()
        return "exec", b""


class PairingNameBarrierAcceptance(unittest.TestCase):
    def setUp(self):
        self.incus = IncusFixture()
        self.barrier = barrier.NameBarrier("fixture", "local", self.incus.name, run=self.incus.run)

    def test_invalidate_deletes_old_marker_only_while_verified_stopped(self):
        self.barrier.invalidate()
        self.assertFalse(self.incus.marker)
        self.assertEqual([args[0] for args, _ in self.incus.calls], ["list", "query", "query", "query"])
        self.assertEqual(self.incus.calls[-1][0][2], "DELETE")

    def test_absent_instance_or_marker_is_structural_and_does_not_delete(self):
        for state, marker in ((None, True), ("Stopped", False)):
            with self.subTest(state=state, marker=marker):
                self.incus.state, self.incus.marker = state, marker
                self.incus.calls.clear()
                self.barrier.invalidate()
                self.assertFalse(any(args[0] == "query" and args[2] == "DELETE" for args, _ in self.incus.calls))

    def test_running_or_failed_listing_deletion_never_means_absent(self):
        for state, failure in (("Running", None), ("Stopped", "list"), ("Stopped", "query-GET"), ("Stopped", "query-DELETE")):
            with self.subTest(state=state, failure=failure):
                self.incus.state, self.incus.fail = state, failure
                self.incus.calls.clear()
                with self.assertRaises(ValueError):
                    self.barrier.invalidate()

    def test_release_uses_exact_snapshot_digest_and_private_root_temp_before_restart(self):
        self.incus.state = "Running"
        self.barrier.release()
        self.assertEqual(len(self.incus.uploads), 1)
        args, data = self.incus.uploads[0]
        self.assertEqual(data, (hashlib.sha256(self.incus.data).hexdigest() + "\n").encode())
        for option, value in (("--uid", "0"), ("--gid", "0"), ("--mode", "0600")):
            self.assertEqual(args[args.index(option) + 1], value)
        self.assertIn("/.pairing-name.ready.", args[-1])
        actions = [args for args, _ in self.incus.calls]
        publish = next(i for i, args in enumerate(actions) if "publish-ready" in args)
        restart = next(i for i, args in enumerate(actions) if "restart" in args)
        self.assertLess(publish, restart)

    def test_wrong_or_malformed_file_never_publishes(self):
        self.incus.state = "Running"
        for data in (b"invalid", b"[]", b"{}", b'{"name":null}', b'{"name":"wrong"}'):
            with self.subTest(data=data):
                self.incus.data = data
                self.incus.calls.clear()
                with self.assertRaises(ValueError):
                    self.barrier.release()
                self.assertFalse(any(args[0] == "file" and args[1] == "push" for args, _ in self.incus.calls))

    def test_failed_pull_push_or_publish_never_restarts_broker(self):
        self.incus.state = "Running"
        for failure in ("file-pull", "file-push", "publish"):
            with self.subTest(failure=failure):
                self.incus.fail = failure
                self.incus.calls.clear()
                with self.assertRaises(ValueError):
                    self.barrier.release()
                self.assertFalse(any("restart" in args for args, _ in self.incus.calls))

    def test_release_requires_running_before_mutating_marker(self):
        for state in (None, "Stopped"):
            with self.subTest(state=state):
                self.incus.state = state
                self.incus.calls.clear()
                with self.assertRaises(ValueError):
                    self.barrier.release()
                self.assertFalse(self.incus.uploads)
