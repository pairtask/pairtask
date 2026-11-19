"""Admission and lifetime tests use the container packet's isolated store."""

from pathlib import Path
import queue
import subprocess
import sys
import threading
import time
import unittest
from unittest import mock

import test_tool_update_acceptance as fixtures
import tool_store
import tool_update


class LaunchAcceptance(unittest.TestCase):
    def setUp(self):
        self.fx = fixtures.UpdateAcceptance()
        self.fx.setUp()
        self.addCleanup(self.fx.doCleanups)

    def test_launch_selects_old_entry_then_refuses_maintenance(self):
        store = self.fx.store
        entry, env = tool_update.resolve_launch("claude", store, {"PATH": "/usr/bin:/bin"})
        self.assertEqual(entry, store.resolve("claude"))
        self.assertTrue(Path(env["PATH"].split(":")[0]).is_absolute())
        self.fx.updates.close("closing")
        with self.assertRaises(tool_store.StoreError) as error:
            tool_update.resolve_launch("claude", store)
        self.assertEqual(error.exception.code, "maintenance")
        self.assertEqual(subprocess.check_output([entry], env=env, text=True).strip(), "1.0.0")

    def test_close_cannot_cross_ready_check_and_exact_selection(self):
        checked, proceed, closing, closed = (threading.Event() for _ in range(4))
        outcomes = queue.Queue()
        original = tool_store.ready

        def paused_ready(*args, **kwargs):
            result = original(*args, **kwargs)
            checked.set()
            if not proceed.wait(5):
                raise RuntimeError("test failed to release readiness barrier")
            return result

        def launch():
            try:
                outcomes.put(tool_update.resolve_launch("claude", self.fx.store))
            except BaseException as error:
                outcomes.put(error)

        def close():
            closing.set()
            try:
                self.fx.updates.close("close-race")
            except BaseException as error:
                outcomes.put(error)
            finally:
                closed.set()

        launcher, closer = threading.Thread(target=launch), threading.Thread(target=close)
        with mock.patch.object(tool_store, "ready", paused_ready):
            launcher.start()
            try:
                self.assertTrue(checked.wait(5), "launcher never reached ready check")
                closer.start()
                self.assertTrue(closing.wait(5))
                self.assertFalse(closed.wait(0.15), "close crossed a launcher's protected selection")
            finally:
                proceed.set()
                launcher.join(5)
                if closer.ident is not None:
                    closer.join(5)
        self.assertFalse(launcher.is_alive() or closer.is_alive())
        result = outcomes.get(timeout=1)
        self.assertIsInstance(result, tuple, repr(result))
        self.assertEqual(result[0], self.fx.store.resolve("claude"))
        self.assertTrue(outcomes.empty())
        self.assertEqual(tool_store.read_state(self.fx.store.state_path)["state"], "maintenance")

    def test_child_keeps_old_path_after_parent_exits_and_two_updates(self):
        root = Path(self.fx.temporary.name)
        ready, go, result = root / "child-ready", root / "child-go", root / "child-result"
        _, env = tool_update.resolve_launch("claude", self.fx.store, {"PATH": "/usr/bin:/bin"})
        child_code = """import pathlib,subprocess,sys,time
ready,go,result = map(pathlib.Path,sys.argv[1:])
ready.write_text('ready')
deadline=time.monotonic()+10
while not go.exists() and time.monotonic()<deadline: time.sleep(.01)
if go.exists(): result.write_text(subprocess.check_output(['claude'],text=True))
"""
        parent_code = """import subprocess,sys
p=subprocess.Popen([sys.executable,'-c',sys.argv[1],*sys.argv[2:]],
                   stdin=subprocess.DEVNULL,stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
print(p.pid)
"""
        parent = subprocess.run([sys.executable, "-c", parent_code, child_code, str(ready), str(go), str(result)],
                                env=env, capture_output=True, text=True, timeout=5, check=True)
        self.assertGreater(int(parent.stdout.strip()), 0)
        self.wait_file(ready)
        try:
            self.fx.commit()
            self.fx.commit("second", "3.0.0")
        finally:
            go.write_text("go")
        self.wait_file(result)
        self.assertEqual(result.read_text().strip(), "1.0.0")

    def wait_file(self, path):
        deadline = time.monotonic() + 5
        while not path.exists() and time.monotonic() < deadline:
            time.sleep(0.01)
        self.assertTrue(path.exists(), f"child did not produce {path.name}")
