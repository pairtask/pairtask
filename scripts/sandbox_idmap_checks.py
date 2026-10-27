"""Measured UID/GID separation between sandbox instances (#183).

Each role's `volatile.idmap.current` is the ID map Incus actually applied: a
JSON list of entries with Isuid/Isgid flags, a host start (Hostid), a
namespace start (Nsid) and a length (Maprange). Two roles are separated only
if no host UID range of one intersects a host UID range of the other, and the
same for GIDs. Comparing the whole map strings, as before, missed partial
overlaps.
"""

from itertools import combinations, product
import json

FAMILIES = (("Isuid", "UID"), ("Isgid", "GID"))
FLAGS = tuple(flag for flag, _ in FAMILIES)
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
    _require_isolated(role, config)
    entries = [_entry(role, entry) for entry in _parse_map(role, config.get("volatile.idmap.current"))]
    _require_families(role, entries)
    return entries


def _require_isolated(role, config):
    if config.get("security.privileged", "false") != "false":
        raise ValueError(f"{role} is privileged")
    if config.get("security.idmap.isolated") != "true":
        raise ValueError(f"{role} has no isolated mapping")


def _parse_map(role, raw):
    entries = _decode(role, raw)
    if not isinstance(entries, list) or not entries:
        raise ValueError(f"{role} has an empty or malformed ID map")
    return entries


def _decode(role, raw):
    if not isinstance(raw, str) or not raw:
        raise ValueError(f"{role} has no measured ID map")
    try:
        return json.loads(raw)
    except ValueError as error:
        raise ValueError(f"{role} has an unreadable ID map: {error}") from None


def _require_families(role, entries):
    for flag, family in FAMILIES:
        if not any(entry[flag] for entry in entries):
            raise ValueError(f"{role} has no {family} range in its ID map")


def _is_count(value):
    if isinstance(value, bool):  # bool is an int in Python, but not a count
        return False
    return isinstance(value, int) and value >= 0


# Each rule is checked in order; the first that fails names the problem.
ENTRY_RULES = (
    (lambda entry: all(isinstance(entry.get(flag), bool) for flag in FLAGS), "Isuid and Isgid must be booleans"),
    (lambda entry: all(_is_count(entry.get(field)) for field in INTEGER_FIELDS),
     "Hostid, Nsid and Maprange must be non-negative integers"),
    (lambda entry: entry["Maprange"] > 0, "the range is empty"),
    (lambda entry: entry["Isuid"] or entry["Isgid"], "it maps neither UIDs nor GIDs"),
)


def _entry(role, entry):
    if not isinstance(entry, dict):
        raise ValueError(f"{role} has a malformed ID map entry: {entry!r}")
    problem = next((message for rule, message in ENTRY_RULES if not rule(entry)), None)
    if problem:
        raise ValueError(f"{role} has a malformed ID map entry, {problem}: {entry!r}")
    return {key: entry[key] for key in FLAGS + INTEGER_FIELDS}


def _overlaps(maps):
    found = []
    for (first, first_map), (second, second_map) in combinations(maps.items(), 2):
        for flag, family in FAMILIES:
            pairs = product(_ranges(first_map, flag), _ranges(second_map, flag))
            found += [f"{first} and {second} {family}s {_span(a)} and {_span(b)}" for a, b in pairs if _intersect(a, b)]
    return found


def _ranges(entries, flag):
    return [(entry["Hostid"], entry["Hostid"] + entry["Maprange"]) for entry in entries if entry[flag]]


def _intersect(a, b):
    return a[0] < b[1] and b[0] < a[1]


def _span(interval):
    return f"[{interval[0]}, {interval[1]})"


def _describe(maps):
    return ", ".join(f"{role}={json.dumps(entries, sort_keys=True)}" for role, entries in maps.items())
