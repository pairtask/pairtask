"""Measured UID/GID separation between sandbox instances (#183).

Each role's `volatile.idmap.current` is the ID map Incus actually applied: a
JSON list of entries with Isuid/Isgid flags, a host start (Hostid), a
namespace start (Nsid) and a length (Maprange). Two roles are separated only
if no host UID range of one intersects a host UID range of the other, and the
same for GIDs. Comparing the whole map strings, as before, missed partial
overlaps.
"""

import json

FAMILIES = (("Isuid", "UID"), ("Isgid", "GID"))
INTEGER_FIELDS = ("Hostid", "Nsid", "Maprange")


def check_disjoint(instances):
    """Raise ValueError unless every pair of roles has disjoint host UID and
    GID ranges. The message names the overlap and carries every role's
    measured map, and nothing else from the instance config."""
    maps = {role: _measured_map(role, instance) for role, instance in sorted(instances.items())}
    overlaps = _overlaps(maps)
    if overlaps:
        raise ValueError("Instances share host IDs: " + "; ".join(overlaps) + ". Measured maps: " + _describe(maps))


def _measured_map(role, instance):
    config = instance.get("expanded_config") or {}
    if config.get("security.privileged", "false") != "false":
        raise ValueError(f"{role} is privileged")
    if config.get("security.idmap.isolated") != "true":
        raise ValueError(f"{role} has no isolated mapping")
    raw = config.get("volatile.idmap.current")
    if not isinstance(raw, str) or not raw:
        raise ValueError(f"{role} has no measured ID map")
    try:
        entries = json.loads(raw)
    except ValueError as error:
        raise ValueError(f"{role} has an unreadable ID map: {error}") from None
    if not isinstance(entries, list) or not entries:
        raise ValueError(f"{role} has an empty or malformed ID map")
    entries = [_entry(role, entry) for entry in entries]
    for flag, family in FAMILIES:
        if not any(entry[flag] for entry in entries):
            raise ValueError(f"{role} has no {family} range in its ID map")
    return entries


def _entry(role, entry):
    if not isinstance(entry, dict):
        raise ValueError(f"{role} has a malformed ID map entry: {entry!r}")
    for flag, _ in FAMILIES:
        if not isinstance(entry.get(flag), bool):
            raise ValueError(f"{role} has an ID map entry without a boolean {flag}: {entry!r}")
    if not (entry["Isuid"] or entry["Isgid"]):
        raise ValueError(f"{role} has an ID map entry for neither UIDs nor GIDs: {entry!r}")
    for field in INTEGER_FIELDS:
        value = entry.get(field)
        # bool is an int in Python; a flag in a number field is still malformed.
        if not isinstance(value, int) or isinstance(value, bool) or value < 0:
            raise ValueError(f"{role} has an ID map entry with an invalid {field}: {entry!r}")
    if entry["Maprange"] == 0:
        raise ValueError(f"{role} has an empty ID map range: {entry!r}")
    return {key: entry[key] for key in ("Isuid", "Isgid") + INTEGER_FIELDS}


def _overlaps(maps):
    roles = list(maps)
    found = []
    for i, first in enumerate(roles):
        for second in roles[i + 1:]:
            for flag, family in FAMILIES:
                for a in _ranges(maps[first], flag):
                    for b in _ranges(maps[second], flag):
                        if a[0] < b[1] and b[0] < a[1]:
                            found.append(f"{first} and {second} {family}s {_span(a)} and {_span(b)}")
    return found


def _ranges(entries, flag):
    return [(entry["Hostid"], entry["Hostid"] + entry["Maprange"]) for entry in entries if entry[flag]]


def _span(interval):
    return f"[{interval[0]}, {interval[1]})"


def _describe(maps):
    return ", ".join(f"{role}={json.dumps(entries, sort_keys=True)}" for role, entries in maps.items())
