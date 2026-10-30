"""Live proof of instant messages between paired workspaces (#222).

Runs on the installed pairing route after both sides hold a confirmed
contact: a message from either side must arrive within seconds, carried by
the live link rather than the next poll, then again after the relays that
carry the link restart and the link reconnects on its own. Only reads watch
the link: `contact sync` would wake it and hide a broken reconnect.
"""

import json
import time
import uuid

INSTANT = 10  # seconds; a poll would take 30 s or 5 min


def require(condition, message):
    if not condition:
        raise ValueError(message)


def contact(run, *args):
    """One `pairtask operator contact` call on a side, as JSON."""
    result = run("contact", *args, "--json")
    require(result.returncode == 0, f"operator contact {args[0]} failed: {result.stdout} {result.stderr}")
    return json.loads(result.stdout)


def page(run, key):
    return contact(run, "list", key).get("contact_page") or {}


def await_received(run, key, message_id, text, timeout=INSTANT):
    """Exactly one inbound row with this ID, carrying exactly this text."""
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        rows = [m for m in page(run, key).get("messages", []) if m.get("direction") == "in" and m.get("id") == message_id]
        if rows:
            require(len(rows) == 1, f"{message_id} arrived {len(rows)} times")
            require(rows[0].get("text") == text, f"{message_id} arrived with other text: {rows[0].get('text')!r}")
            return
        time.sleep(0.2)
    raise ValueError(f"{message_id} did not arrive within {timeout}s")


def await_link(run, key, after=None, timeout=INSTANT * 3):
    """The side's link is live, read-only; with after, from a dial made
    since that attempt time."""
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        sync = page(run, key).get("sync") or {}
        if sync.get("live") and (after is None or sync.get("last_attempt") != after):
            return sync
        time.sleep(0.5)
    raise ValueError(f"no live link with {key} within {timeout}s")


def exchange(sides, label):
    """A message each way, with its own ID; each must arrive within INSTANT
    seconds, exactly once."""
    for (sender, recipient_key), (receiver, sender_key) in (sides, sides[::-1]):
        message_id = f"live-{uuid.uuid4().hex[:12]}"
        text = f"{label} {message_id}"
        started = time.monotonic()
        contact(sender, "send", recipient_key, text, "--message-id", message_id)
        await_received(receiver, sender_key, message_id, text, timeout=INSTANT - (time.monotonic() - started))
        elapsed = time.monotonic() - started
        require(elapsed <= INSTANT, f"{message_id} took {elapsed:.1f}s, over {INSTANT}s")
        print(f"PASS: {label}: delivered in {elapsed:.1f}s.", flush=True)


def prove_live(secured, host, secured_key, host_key, restart_relays):
    """secured and host run `pairtask operator contact …` on each side;
    restart_relays restarts what carries the link."""
    sides = ((secured, host_key), (host, secured_key))
    links = ((secured, host_key), (host, secured_key))
    synced = [await_link(run, key) for run, key in links]
    dialer = next(i for i, s in enumerate(synced) if s.get("dials"))
    exchange(sides, "live link")
    restart_relays()
    run, key = links[dialer]
    await_link(run, key, after=synced[dialer].get("last_attempt"))
    exchange(sides, "after reconnect")
    print("PASS: instant messages both ways over the live link, again after it reconnected on its own.", flush=True)
