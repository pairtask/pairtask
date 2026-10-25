#!/usr/bin/env python3
"""Prove prepared-operator reuse, fresh identity, and symlink rejection in CI."""

import argparse
from contextlib import ExitStack
import json
import re
import signal
import subprocess

import sandbox_operator_prepared as prepared
from sandbox_operator_runtime import Operator


def require(condition, message):
    if not condition:
        raise AssertionError(message)


def inspect_operator(operator):
    return prepared.inspect(subprocess.run, operator.remote[:-1], operator.project, operator.name)


def require_distinct_identities(first, second):
    for state in (first, second):
        require(re.fullmatch(r"[0-9a-f]{32}", state.machine_id) is not None,
                "launched operator has no valid machine-id")
        require(state.machine_id != "0" * 32, "machine-id is all zeroes")
    require(first.machine_id != second.machine_id, "cached launches share a machine-id")
    require(set(first.ssh_host_keys) == set(second.ssh_host_keys), "SSH key types differ between launches")
    for name in first.ssh_host_keys:
        require(first.ssh_host_keys[name] != second.ssh_host_keys[name],
                "cached launches share SSH host key " + name)


def require_symlink_rejected(operator):
    # An empty target makes this fail if recursive pull silently follows the
    # link and turns it into an ordinary empty directory on the host.
    operator.execute("mkdir", "/operator-link-target")
    operator.execute("rmdir", "/operator/config")
    operator.execute("ln", "-s", "/operator-link-target", "/operator/config")
    try:
        state = inspect_operator(operator)
        prepared.verify_launch(state, prepared.expected_binary_sha256(operator.arch))
    except prepared.Contaminated:
        return
    raise AssertionError("the real file API inspection accepted a symlinked config directory")


def prove(args):
    store = prepared.store_from_env(args.remote)
    require(store is not None, "this proof requires an enabled prepared-image cache")
    server = json.loads(subprocess.check_output(["incus", "query", args.remote + ":/1.0"]))["environment"]
    operators = [Operator(args.remote, server, args.pool, args.network).using(prepared_store=store)
                 for _ in range(2)]
    first = operators[0]
    key = prepared.recipe_key(first.arch, first.base_fingerprint(),
                              prepared.operator_lock_bytes(), prepared.recipe_bytes())
    fingerprint = store.find(key)
    require(fingerprint is not None, "proof must reuse the existing job cache, not build another image")
    with ExitStack() as cleanup:
        states = []
        for operator in operators:
            cleanup.callback(operator.cleanup)
            operator.create()
            actual = operator.run("config", "get", operator.target, "volatile.base_image",
                                  capture_output=True, text=True).stdout.strip()
            require(actual == fingerprint, "operator did not launch from the prepared image")
            states.append(inspect_operator(operator))
        require_distinct_identities(*states)
        require_symlink_rejected(first)
    print("PASS: two cached launches have fresh identities; real file inspection rejects a config symlink.", flush=True)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--remote", default="local")
    parser.add_argument("--pool", default="default")
    parser.add_argument("--network", default="incusbr0")
    prove(parser.parse_args())


def interrupted(*_):
    raise KeyboardInterrupt


if __name__ == "__main__":
    signal.signal(signal.SIGTERM, interrupted)
    main()
