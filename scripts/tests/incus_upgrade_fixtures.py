"""Shared observations and manifests for pure-mock upgrade tests."""
import copy
from dataclasses import asdict
import importlib.util
from pathlib import Path
import sys

SCRIPTS = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(SCRIPTS))
import sandbox_incus_upgrade as upgrade


def load_host():
    spec = importlib.util.spec_from_file_location("upgrade_acceptance_host", SCRIPTS / "sandbox-host.py")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module

HOST = load_host()


def observation():
    packages = {name: upgrade.Package("7.1-1", "7.5-1", upgrade.ORIGIN) for name in upgrade.PACKAGES}
    return upgrade.Observation(
        server_version="7.1", packages=packages,
        instances=[{"project": "another-project", "name": "kept", "status": "Stopped"}],
        pools=[{"name": "default", "driver": "dir", "source": "/var/lib/incus/storage-pools/default",
                "resolved": "/var/lib/incus/storage-pools/default"}], incus_root=upgrade.INCUS_DIR,
        holds=list(upgrade.PACKAGES) + ["zfsutils-linux"],
        units={name: upgrade.Unit("active", "enabled") for name in upgrade.UNITS},
        backup_free_bytes=10**12, incus_bytes=1024,
        upgrades={name: True for name in upgrade.PACKAGES})


def pins_for(obs):
    return {name: package.candidate for name, package in obs.packages.items()}


def transaction_for(pins):
    return [("Inst", name, version) for name, version in sorted(pins.items())]


def manifest_for(obs):
    pins = pins_for(obs)
    return {"schema": upgrade.MANIFEST_SCHEMA, "target": dict(upgrade.TARGET),
            "server_version": obs.server_version, "expected_server": "7.5", "pins": pins,
            "transaction": transaction_for(pins), "holds": list(obs.holds),
            "units": {name: asdict(unit) for name, unit in obs.units.items()},
            "instances": copy.deepcopy(obs.instances), "pools": copy.deepcopy(obs.pools),
            "packages": {name: asdict(package) for name, package in obs.packages.items()},
            "blockers": []}
