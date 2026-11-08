"""Known-peer discovery on the host (#284), Python 3.9 stdlib only.

A paired workspace whose address changed is looked for in two places: the
LAN, through a minimal mDNS (RFC 6762) advertiser and browser for
_pairtask._tcp.local, and the tailnet, through `tailscale status --json`.
What either finds is only a HOST:PORT to try: the broker alone decides, by
the contact's pinned identity, whether a workspace it knows is there.

An advert says only that a pairtask listener is at an address and port: a
random instance and host name, new for every process, TXT v=1 and nothing
else. Every parse is bounded (peer_discovery_wire); a packet that is
malformed or over a bound is dropped without an error.
"""

from collections import OrderedDict, namedtuple
import datetime
import ipaddress
import json
import os
import re
import secrets
import select
import shutil
import socket
import struct
import subprocess
import sys
import time

HERE = os.path.dirname(os.path.abspath(__file__))
if HERE not in sys.path:
    sys.path.insert(0, HERE)  # loaded by path (tests, runpy): the codec sits beside it

from peer_discovery_wire import (  # noqa: E402
    A, AAAA, ANY, PACKET_MAX, PTR, RECORD_MAX, SERVICE, SRV, TTL_MAX, TXT,
    Entry, Malformed, Message, Record, encode_name, encode_record, parse, query, response)

__all__ = ["Advert", "Browser", "Session", "lease_from", "merge", "parse", "tailnet_candidates",
           "tailscale_status", "Entry", "Malformed", "Message", "Record"]

GROUP, PORT = "224.0.0.251", 5353
CACHE_MAX = 32         # service bundles a browser keeps
CANDIDATES_MAX = 8     # endpoints in one submission
HOST_ADDRESSES_MAX = 8
ADVERT_ADDRESSES_MAX = 16
LEASE_MAX = 90         # seconds a lease holds at most, by this helper's own clock
POLL_SECONDS = 5       # interface addresses are polled: a change re-announces within 10 s
QUERY_GAP_MAX = 30
TAILSCALE_TIMEOUT = 5
ADDRESS_KINDS = (A, AAAA)
ASKED_KINDS = (PTR, SRV, TXT, A, AAAA, ANY)

Lease = namedtuple("Lease", "advertise browse ports expires_at deadline")


# --- Addresses and endpoints -----------------------------------------------

def address(value):
    """The IP in value, without an IPv6 zone, or None."""
    try:
        return ipaddress.ip_address(str(value).split("%", 1)[0])
    except ValueError:
        return None


def special(ip):
    """Loopback, link-local, multicast, unspecified, reserved or IPv4-mapped."""
    return any((ip.is_loopback, ip.is_link_local, ip.is_multicast, ip.is_unspecified, ip.is_reserved,
                getattr(ip, "ipv4_mapped", None) is not None))


def eligible(value):
    """A unicast address another machine could dial."""
    ip = address(value)
    return ip is not None and not special(ip)


def is_ipv6(ip):
    return ip is not None and ip.version == 6


def endpoint(host, port):
    """HOST:PORT, with an IPv6 literal in brackets."""
    ip = address(host)
    return ("[%s]:%d" if is_ipv6(ip) else "%s:%d") % (ip or host, port)


def is_port(value):
    return type(value) is int and 0 < value < 65536


def unique(values, limit=CANDIDATES_MAX):
    result = []
    for value in values:
        if value not in result:
            result.append(value)
    return result[:limit]


def merge(*groups):
    """One submission: the groups in order (tailnet names first), de-duplicated, bounded."""
    return unique(value for group in groups for value in group)


# --- Browser ---------------------------------------------------------------

def in_service(name):
    """An instance name under the service."""
    return isinstance(name, str) and name.endswith("." + SERVICE)


def names_instance(record):
    return record.name == SERVICE and in_service(record.value)


def has_port(record):
    return record.value is not None and bool(record.value[1])


def locates_instance(record):
    return in_service(record.name) and has_port(record)


def is_service_pointer(record):
    """The service's PTR to one of its instances."""
    return record.kind == PTR and names_instance(record)


def is_service_location(record):
    """An instance's SRV, with a port."""
    return record.kind == SRV and locates_instance(record)


def is_address(record):
    return record.kind in ADDRESS_KINDS and record.value is not None


def is_answer(message):
    return message is not None and message.response


class Service:
    """One instance's bundle: its PTR and SRV, by expiry."""

    def __init__(self):
        self.ptr = self.srv = 0.0
        self.target, self.port = None, 0

    def alive(self, now):
        return self.ptr > now and self.srv > now

    def stale(self, now):
        return self.ptr <= now and self.srv <= now


class Browser:
    """What the LAN answered for the service: PTR, SRV and A/AAAA joined
    into endpoints. The cache holds at most max_entries service bundles and
    drops the least recently seen; addresses are kept only for the host
    names a known SRV points at."""

    def __init__(self, max_entries=CACHE_MAX):
        self.max_entries = max(1, int(max_entries))
        self.services = OrderedDict()
        self.hosts = OrderedDict()
        self.own = None  # this process's Advert, never a candidate

    def receive(self, packet, now):
        self.accept(parse(packet), now)

    def accept(self, message, now):
        if not is_answer(message):
            return
        for record in message.records:
            self.service_entry(record, now)
        self.address_entries(message.records, now)
        self.expire(now)

    def service_entry(self, record, now):
        if is_service_pointer(record):
            self.service_record(record.value, record.ttl, now, None)
        elif is_service_location(record):
            self.service_record(record.name, record.ttl, now, record.value)

    def address_entries(self, records, now):
        """Addresses only for the host names a known SRV points at."""
        targets = {service.target for service in self.services.values()}
        for record in records:
            if is_address(record) and record.name in targets:
                self.address_record(record.name, record.value, record.ttl, now)

    def service_record(self, instance, ttl, now, srv):
        if ttl == 0:  # goodbye
            self.services.pop(instance, None)
            return
        service = self.services.pop(instance, None) or Service()
        if srv is None:
            service.ptr = now + ttl
        else:
            service.target, service.port = srv
            service.srv = now + ttl
        self.services[instance] = service  # most recently seen last
        while len(self.services) > self.max_entries:
            self.services.popitem(last=False)

    def address_record(self, host, value, ttl, now):
        addresses = self.hosts.pop(host, OrderedDict())
        addresses.pop(value, None)
        if ttl:
            addresses[value] = now + ttl
        while len(addresses) > HOST_ADDRESSES_MAX:
            addresses.popitem(last=False)
        if addresses:
            self.hosts[host] = addresses
        while len(self.hosts) > self.max_entries:
            self.hosts.popitem(last=False)

    def expire(self, now):
        for instance in [i for i, s in self.services.items() if s.stale(now)]:
            del self.services[instance]
        for host in list(self.hosts):
            self.expire_host(host, now)

    def expire_host(self, host, now):
        addresses = self.hosts[host]
        for value in [v for v, expiry in addresses.items() if expiry <= now]:
            del addresses[value]
        if not addresses:
            del self.hosts[host]

    def candidates(self, now):
        """Unexpired endpoints, most recently seen service first, at most
        CANDIDATES_MAX, never this process's own advert."""
        found = []
        for instance, service in reversed(self.services.items()):
            if self.listed(instance, service, now):
                found += self.endpoints_of(service, now)
        own = self.own.endpoints() if self.own else set()
        return unique(value for value in found if value not in own)

    def listed(self, instance, service, now):
        return service.alive(now) and not self.is_own(instance)

    def endpoints_of(self, service, now):
        addresses = self.hosts.get(service.target, {})
        return [endpoint(value, service.port) for value, expiry in addresses.items()
                if expiry > now and eligible(value)]

    def is_own(self, instance):
        return self.own is not None and instance == self.own.instance


# --- Advertiser --------------------------------------------------------------

def address_entry(host, value):
    ip = address(value)
    return Entry(host, A if ip.version == 4 else AAAA, ip.packed)


class Advert:
    """This process's presence: a random 16-hex instance and .local host,
    the published port, TXT v=1 and the eligible addresses; no names, keys
    or contacts."""

    def __init__(self, port):
        self.instance = secrets.token_hex(8) + "." + SERVICE
        self.host = secrets.token_hex(8) + ".local"
        self.port = port
        self.addresses = []

    def entries(self, addresses):
        location = struct.pack("!3H", 0, 0, self.port) + encode_name(self.host)
        return [Entry(SERVICE, PTR, encode_name(self.instance), flush=False),
                Entry(self.instance, SRV, location),
                Entry(self.instance, TXT, b"\x03v=1")] + [address_entry(self.host, v) for v in addresses]

    def packet(self, ttl, addresses=None):
        """The advert, or its goodbye at ttl 0, with these addresses (default all)."""
        chosen = self.addresses if addresses is None else addresses
        return response([encode_record(entry, ttl) for entry in self.entries(chosen)])

    def asked(self, message):
        """Whether a query asks for the service or this advert's names."""
        names = (SERVICE, self.instance, self.host)
        return any(name in names and kind in ASKED_KINDS for name, kind in message.questions)

    def endpoints(self):
        return {endpoint(value, self.port) for value in self.addresses}


# --- Interfaces ----------------------------------------------------------------

SIOCGIFADDR = 0x8915  # Linux
IPV6_SKIPPED = 0x01 | 0x20 | 0x40  # temporary, deprecated, tentative
IFCONFIG_SKIPPED = {"temporary", "deprecated", "tentative", "detached"}
IFCONFIG_FAMILIES = ("inet", "inet6")


def interface_addresses():
    """This host's eligible unicast addresses, by interface; {} when none."""
    try:
        found = linux_addresses() if sys.platform.startswith("linux") else ifconfig_addresses()
    except (OSError, ValueError, subprocess.SubprocessError):
        found = {}
    result = {}
    for name, values in sorted(found.items()):
        kept = sorted({str(address(v)) for v in values if eligible(v)})
        if kept:
            result[name] = kept
    return result or fallback_addresses()


def linux_addresses():
    """IPv4 by ioctl, one per interface; IPv6 from /proc, stable ones only."""
    found = {}
    linux_ipv4(found)
    linux_ipv6(found)
    return found


def linux_ipv4(found):
    with socket.socket(socket.AF_INET, socket.SOCK_DGRAM) as probe:
        for _, name in socket.if_nameindex():
            value = interface_ipv4(probe, name)
            if value:
                found.setdefault(name, []).append(value)


def interface_ipv4(probe, name):
    """The interface's IPv4 address, or None without one."""
    import fcntl
    try:
        raw = fcntl.ioctl(probe.fileno(), SIOCGIFADDR, struct.pack("256s", name.encode()[:15]))
    except OSError:
        return None
    return socket.inet_ntoa(raw[20:24])


def linux_ipv6(found):
    for fields in (line.split() for line in file_lines("/proc/net/if_inet6")):
        if stable_ipv6(fields):
            found.setdefault(fields[5], []).append(str(ipaddress.IPv6Address(bytes.fromhex(fields[0]))))


def file_lines(path):
    try:
        with open(path) as stream:
            return stream.read().splitlines()
    except OSError:
        return []


def stable_ipv6(fields):
    """An if_inet6 line that is not temporary, deprecated or tentative."""
    return len(fields) == 6 and not int(fields[4], 16) & IPV6_SKIPPED


def ifconfig_addresses():
    """macOS and other BSDs: the inet and inet6 lines of ifconfig."""
    command = shutil.which("ifconfig") or "/sbin/ifconfig"
    output = subprocess.run([command], capture_output=True, text=True, timeout=3, check=False).stdout
    return ifconfig_parse(output)


def ifconfig_parse(output):
    found, name = {}, None
    for line in output.splitlines():
        name = interface_header(line) or name
        value = ifconfig_address(line.split())
        if name and value:
            found.setdefault(name, []).append(value)
    return found


def interface_header(line):
    """The interface an unindented ifconfig line starts, else None."""
    return line.split(":", 1)[0] if line[:1].strip() else None


def ifconfig_address(words):
    """The address of an inet or inet6 line, unless it is temporary or going away."""
    if len(words) < 2 or words[0] not in IFCONFIG_FAMILIES:
        return None
    return None if IFCONFIG_SKIPPED & set(words) else words[1]


def fallback_addresses():
    """The source address of the default routes; a connect sends nothing."""
    found = [route_source(family, target) for family, target in
             ((socket.AF_INET, ("192.0.2.1", 9)), (socket.AF_INET6, ("2001:db8::1", 9)))]
    kept = [str(address(value)) for value in found if eligible(value)]
    return {"default": kept} if kept else {}


def route_source(family, target):
    try:
        with socket.socket(family, socket.SOCK_DGRAM) as probe:
            probe.connect(target)
            return probe.getsockname()[0]
    except OSError:
        return None


def ipv4(values):
    return [value for value in values if address(value).version == 4]


# --- Sockets and session -----------------------------------------------------

class Channel:
    """One UDP socket on the mDNS group and port, shared with any system
    responder (mDNSResponder, avahi) through SO_REUSEADDR/SO_REUSEPORT."""

    def __init__(self, group=GROUP, port=PORT):
        self.group, self.port, self.joined = group, port, set()
        self.sock = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
        try:
            self.setup()
        except OSError:
            self.sock.close()
            raise

    def setup(self):
        self.sock.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
        self.reuse_port()
        self.sock.bind(("", self.port))
        self.sock.setsockopt(socket.IPPROTO_IP, socket.IP_MULTICAST_TTL, 255)
        self.sock.setsockopt(socket.IPPROTO_IP, socket.IP_MULTICAST_LOOP, 1)
        self.sock.setblocking(False)
        self.join([])

    def reuse_port(self):
        if not hasattr(socket, "SO_REUSEPORT"):
            return
        try:
            self.sock.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEPORT, 1)
        except OSError:
            pass

    def join(self, interfaces):
        """Membership on the default interface and on each IPv4 interface address."""
        for interface in ["0.0.0.0"] + list(interfaces):
            if interface not in self.joined:
                self.joined.add(interface)
                self.membership(interface)

    def membership(self, interface):
        try:
            membership = socket.inet_aton(self.group) + socket.inet_aton(interface)
            self.sock.setsockopt(socket.IPPROTO_IP, socket.IP_ADD_MEMBERSHIP, membership)
        except OSError:
            pass  # already joined on that interface, or it went away

    def send(self, packet, interface="0.0.0.0"):
        try:
            self.sock.setsockopt(socket.IPPROTO_IP, socket.IP_MULTICAST_IF, socket.inet_aton(interface))
            self.sock.sendto(packet, (self.group, self.port))
            return True
        except OSError:
            return False

    def receive(self, timeout):
        """The packets waiting, after at most timeout seconds; oversized ones are cut and dropped by parse."""
        packets = []
        while self.ready(0 if packets else timeout) and len(packets) < RECORD_MAX:
            packet = self.read()
            if packet is None:
                break
            packets.append(packet)
        return packets

    def ready(self, timeout):
        try:
            return bool(select.select([self.sock], [], [], max(timeout, 0))[0])
        except (OSError, ValueError):
            return False

    def read(self):
        try:
            return self.sock.recvfrom(PACKET_MAX + 1)[0]
        except OSError:
            return None

    def close(self):
        self.sock.close()


def due(times, now):
    """Whether the first scheduled time has come."""
    return bool(times) and now >= times[0]


class Session:
    """mDNS for the lease's current flags on one channel: the advert,
    announced and answered while advertising, and a browser fed by every
    answer while browsing. Interface addresses are polled; a change
    re-announces."""

    def __init__(self, advert, group=GROUP, port=PORT, clock=time.monotonic):
        self.advert, self.clock = advert, clock
        self.interfaces = interface_addresses()
        self.channel = Channel(group, port)
        self.refresh_addresses()
        self.advertising, self.browser = False, None
        self.announce_at, self.query_at, self.query_gap = [], 0.0, 1.0
        self.poll_at, self.answered = clock() + POLL_SECONDS, float("-inf")

    def refresh_addresses(self):
        self.channel.join(value for values in self.interfaces.values() for value in ipv4(values))
        if self.advert is not None:
            every = [value for values in self.interfaces.values() for value in values]
            self.advert.addresses = unique(every, ADVERT_ADDRESSES_MAX)

    def update(self, advertise, browse):
        self.set_advertising(bool(advertise) and self.advert is not None)
        self.set_browsing(browse)

    def set_advertising(self, advertise):
        if advertise and not self.advertising:
            self.schedule_announcements(self.clock())
        if self.advertising and not advertise:
            self.send_advert(0)
        self.advertising = advertise

    def schedule_announcements(self, now):
        self.announce_at = [now, now + 1]  # two announcements, a second apart

    def set_browsing(self, browse):
        if not browse:
            self.browser = None
        elif self.browser is None:
            self.browser = Browser()
            self.browser.own = self.advert
            self.query_at, self.query_gap = self.clock(), 1.0

    def step(self, timeout):
        self.timers(self.clock())
        for packet in self.channel.receive(timeout):
            self.handle(packet)

    def timers(self, now):
        if now >= self.poll_at:
            self.poll(now)
        if self.announcement_due(now):
            self.announce(now)
        if self.query_due(now):
            self.ask(now)

    def announcement_due(self, now):
        return self.advertising and due(self.announce_at, now)

    def query_due(self, now):
        return self.browser is not None and now >= self.query_at

    def announce(self, now):
        self.announce_at.pop(0)
        self.answer(now)

    def answer(self, now):
        self.send_advert(TTL_MAX)
        self.answered = now

    def ask(self, now):
        for interface in self.send_interfaces():
            self.channel.send(query(), interface)
        self.query_at, self.query_gap = now + self.query_gap, min(self.query_gap * 2, QUERY_GAP_MAX)

    def poll(self, now):
        self.poll_at = now + POLL_SECONDS
        current = interface_addresses()
        if current == self.interfaces:
            return
        self.interfaces = current
        self.refresh_addresses()
        if self.advertising:
            self.schedule_announcements(now)

    def handle(self, packet):
        message = parse(packet)
        if message is None:
            return
        now = self.clock()
        if message.response:
            self.observe(message, now)
        elif self.should_answer(message, now):
            self.answer(now)

    def should_answer(self, message, now):
        """A query for this advert while advertising, at most once a second (RFC 6762 6)."""
        return self.advertising and self.answerable(message, now)

    def answerable(self, message, now):
        return self.advert.asked(message) and now - self.answered >= 1

    def observe(self, message, now):
        if self.browser is not None:
            self.browser.accept(message, now)

    def send_interfaces(self):
        found = [value for values in self.interfaces.values() for value in ipv4(values)]
        return found or ["0.0.0.0"]

    def send_advert(self, ttl):
        """On each IPv4 interface, with only that interface's addresses."""
        groups = [(value, values) for values in self.interfaces.values() for value in ipv4(values)]
        for interface, addresses in groups or [("0.0.0.0", self.advert.addresses)]:
            self.channel.send(self.advert.packet(ttl, addresses), interface)

    def candidates(self):
        return self.browser.candidates(self.clock()) if self.browser is not None else []

    def stop(self):
        """Goodbye (TTL 0) while advertising, then close."""
        if self.advertising:
            self.send_advert(0)
            self.advertising = False
        self.browser = None
        self.channel.close()


# --- Tailnet -----------------------------------------------------------------

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


def tailnet_candidates(status, ports):
    """Endpoints from `tailscale status --json`: each online peer's DNSName,
    then its TailscaleIPs, crossed with the lease's ports, at most
    CANDIDATES_MAX. Self is never a peer; display names are never used."""
    peers = tailnet_peers(status)
    if not peers:
        return []
    usable = [port for port in ports if is_port(port)]
    return unique(endpoint(host, port) for host in tailnet_hosts(peers, tailnet_self(status)) for port in usable)


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


# --- Lease -----------------------------------------------------------------

STAMP = re.compile(r"(\d{4})-(\d\d)-(\d\d)[Tt](\d\d):(\d\d):(\d\d)(\.\d+)?([Zz]|[+-]\d\d:\d\d)\Z")


def zone_minutes(zone):
    """The offset of an RFC 3339 zone, Z or ±HH:MM, in minutes."""
    if zone in "Zz":
        return 0
    return (1 if zone[0] == "+" else -1) * (int(zone[1:3]) * 60 + int(zone[4:6]))


def timestamp(value):
    """Seconds since the epoch for an RFC 3339 time, or None."""
    match = STAMP.match(value) if isinstance(value, str) else None
    if match is None:
        return None
    zone = datetime.timezone(datetime.timedelta(minutes=zone_minutes(match.group(8))))
    try:
        moment = datetime.datetime(*[int(part) for part in match.groups()[:6]], tzinfo=zone)
    except ValueError:
        return None
    return moment.timestamp() + float(match.group(7) or 0)


def lease_ports(data):
    ports = data.get("ports") if isinstance(data.get("ports"), list) else []
    return unique((port for port in ports if is_port(port)), CANDIDATES_MAX)


def lease_from(answer, asked_at, now, wall):
    """The lease in an operator answer, or None when there is none. Its
    deadline is on the monotonic clock: the broker's expiry, and never more
    than LEASE_MAX after the request was sent."""
    data = answer.get("contact_discovery") if isinstance(answer, dict) else None
    expiry = timestamp(data.get("expires_at")) if isinstance(data, dict) else None
    if expiry is None:
        return None
    deadline = min(now + (expiry - wall), asked_at + LEASE_MAX)
    return Lease(data.get("advertise") is True, data.get("browse") is True, lease_ports(data),
                 data.get("expires_at"), deadline)
