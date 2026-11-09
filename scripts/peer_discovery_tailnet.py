"""The tailnet side of host discovery (#284): what `tailscale status --json`
says about online peers, as endpoints for a moved contact and as devices
for the operator's picker. Devices are not pairtask listeners; the pairing
exchange decides who is there."""
import json
import os
import re
import subprocess
import sys

HERE = os.path.dirname(os.path.abspath(__file__))
if HERE not in sys.path:
    sys.path.insert(0, HERE)  # loaded by path (tests, runpy): the helpers sit beside it

from peer_discovery_net import CANDIDATES_MAX, INVENTORY_MAX, address, eligible, endpoint, is_port, unique  # noqa: E402

DEVICES_MAX = 32       # tailnet devices reported for the picker
TAILSCALE_TIMEOUT = 5

DNS_LABEL = re.compile(r"[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\Z")


def dns_name(value):
    """A peer's configured MagicDNS name without its trailing dot, or None."""
    if not isinstance(value, str):
        return None
    name = value.rstrip(".").lower()
    return name if hostname(name) else None


def hostname(name):
    """At most 253 bytes of at least two DNS labels, and not an IP literal."""
    labels = name.split(".")
    if any((len(name) > 253, len(labels) < 2, address(name) is not None)):
        return False
    return all(DNS_LABEL.match(label) for label in labels)


def online(peer):
    """A peer entry not marked offline; a missing Online counts as online."""
    return isinstance(peer, dict) and peer.get("Online", True) is not False


def tailnet_peers(status):
    """The online peers of `tailscale status --json`, in key order."""
    peers = status.get("Peer") if isinstance(status, dict) else None
    if not isinstance(peers, dict):
        return []
    return [peer for _, peer in sorted(peers.items()) if online(peer)]


def tailnet_self(status):
    own = status.get("Self")
    return dns_name(own.get("DNSName")) if isinstance(own, dict) else None


def peer_ips(peer):
    listed = peer.get("TailscaleIPs")
    return [str(address(v)) for v in (listed if isinstance(listed, list) else []) if eligible(v)]


def tailnet_devices(status):
    """Online tailnet peers for the operator's picker, at most DEVICES_MAX:
    each one's MagicDNS name and first Tailscale address. Devices, not
    pairtask listeners; nothing here is verified."""
    own = tailnet_self(status)
    devices = (device_of(peer, own) for peer in tailnet_peers(status))
    return [device for device in devices if device is not None][:DEVICES_MAX]


def device_of(peer, own):
    """One peer as a picker row, or None for this machine and for a peer
    with neither a name nor an address."""
    name, ips = dns_name(peer.get("DNSName")), peer_ips(peer)
    if name is not None and name == own:
        return None
    if not (name or ips):
        return None
    return {"name": name or "", "address": ips[0] if ips else ""}


def tailnet_candidates(status, ports):
    """Endpoints from `tailscale status --json`: each online peer's DNSName,
    then its TailscaleIPs, crossed with the lease's ports, at most
    CANDIDATES_MAX. Self is never a peer; display names are never used."""
    return tailnet_inventory(status, ports)[:CANDIDATES_MAX]


def tailnet_inventory(status, ports):
    """Every endpoint tailnet_candidates would offer, not only the first
    CANDIDATES_MAX, bounded by INVENTORY_MAX."""
    peers = tailnet_peers(status)
    if not peers:
        return []
    usable = [port for port in ports if is_port(port)]
    return unique((endpoint(host, port) for host in tailnet_hosts(peers, tailnet_self(status)) for port in usable), INVENTORY_MAX)


def tailnet_hosts(peers, own):
    """Every peer's DNSName, then every peer's TailscaleIPs, de-duplicated;
    a peer listed under this machine's own name is skipped."""
    names, ips = [], []
    for peer in peers:
        name = dns_name(peer.get("DNSName"))
        if name is not None and name == own:
            continue
        names += [name] if name else []
        ips += peer_ips(peer)
    return unique(names + ips, 10 ** 4)


def tailscale_status(command):
    """The parsed `tailscale status --json`, or None; run as given, no shell."""
    try:
        result = subprocess.run([command, "status", "--json"], capture_output=True, text=True,
                                timeout=TAILSCALE_TIMEOUT, check=False)
        data = json.loads(result.stdout) if result.returncode == 0 else None
    except (OSError, ValueError, subprocess.SubprocessError):
        return None
    return data if isinstance(data, dict) else None
