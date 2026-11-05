"""Reject plans that expose operator files or delete persistent data volumes."""

from pathlib import Path


PROTECTED_VOLUMES = {"workspace-data", "agent-home", "secured-state", "docker-data"}


def validate_plan(plan, directory, operator_home, allow_destroy=False):
    if not allow_destroy:
        require_retained_volumes(plan.get("resource_changes", []))
    resources = plan.get("planned_values", {}).get("root_module", {}).get("resources", [])
    for source in host_sources(resources):
        require_private_path(Path(source), directory, operator_home)


def deletion_only(plan):
    """Every change the plan makes deletes, and it deletes something: what a
    saved --destroy plan may still apply on an unsupported Incus (#312)."""
    changes = plan.get("resource_changes") if isinstance(plan, dict) else None
    if not isinstance(changes, list):
        return False
    actions = [change_actions(change) for change in changes]
    return ["delete"] in actions and all(a in (["delete"], ["no-op"]) for a in actions)


def change_actions(change):
    """A resource change's actions, or None when it has none readable."""
    detail = change.get("change") if isinstance(change, dict) else None
    actions = detail.get("actions") if isinstance(detail, dict) else None
    return actions if isinstance(actions, list) else None


def require_retained_volumes(changes):
    for change in changes:
        if change["type"] != "incus_storage_volume":
            continue
        before = change["change"].get("before") or {}
        if before.get("name") in PROTECTED_VOLUMES and "delete" in change["change"]["actions"]:
            raise ValueError("Provisioning would delete persistent data; refusing this plan")


def host_sources(resources):
    for resource in resources:
        if resource["type"] not in ("incus_profile", "incus_instance"):
            continue
        yield from device_sources(resource.get("values", {}).get("device", []))


def device_sources(devices):
    for device in devices:
        source = device.get("properties", {}).get("source") or ""
        if source.startswith("/"):
            yield source


def require_private_path(source, directory, operator_home):
    for private in (directory, operator_home):
        if private.resolve().is_relative_to(source.resolve()):
            raise ValueError(f"Host mount {source} exposes operator state/settings; select a narrower project directory")
