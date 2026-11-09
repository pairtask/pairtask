#!/usr/bin/env python3
"""Browsing through the system DNS-SD daemon (Bonjour on macOS) over its C
API, stdlib ctypes only. The daemon browses for the service on every
interface, resolves each instance and looks its host up; the replies feed
the same Browser cache the multicast path fills, so what the helper reports
does not depend on the backend, and no multicast socket is opened for it.

An instance or an address stays listed while any interface still reports
it; the daemon's add and remove replies decide, bounded by LIFETIME should
the daemon fall silent. One resolve per (interface, instance), stopped at
its first reply; one address lookup per (interface, host), kept open for
additions and removals."""

from collections import namedtuple
import ctypes
import socket
import sys

from peer_discovery_wire import Malformed, SERVICE, txt_value

LIBRARY = "/usr/lib/libSystem.B.dylib"
REGTYPE = b"_pairtask._tcp"
DOMAIN = b"local."
ALL_INTERFACES = 0
FLAG_ADD = 0x2
PROTOCOLS = 0x1 | 0x2        # kDNSServiceProtocol_IPv4 | kDNSServiceProtocol_IPv6
AF_INET6_DARWIN = 30
SOCKADDR_MAX = 28
LIFETIME = 4500              # seconds an instance stays without the daemon's removal: the mDNS PTR TTL
ADDRESSES_MAX = 16           # values tracked per host
ERRORS = {-65537: "unknown", -65538: "no such name", -65540: "bad parameter", -65545: "not initialized",
          -65550: "firewall", -65553: "refused", -65554: "no such record", -65563: "service not running",
          -65568: "timeout", -65569: "defunct connection", -65570: "policy denied"}
SYSTEMIC = {-65545, -65550, -65553, -65563, -65569, -65570}  # the daemon or its connection, not one record

Ref = ctypes.c_void_p
BROWSE_REPLY = ctypes.CFUNCTYPE(None, Ref, ctypes.c_uint32, ctypes.c_uint32, ctypes.c_int32,
                                ctypes.c_char_p, ctypes.c_char_p, ctypes.c_char_p, ctypes.c_void_p)
RESOLVE_REPLY = ctypes.CFUNCTYPE(None, Ref, ctypes.c_uint32, ctypes.c_uint32, ctypes.c_int32,
                                 ctypes.c_char_p, ctypes.c_char_p, ctypes.c_uint16, ctypes.c_uint16,
                                 ctypes.POINTER(ctypes.c_ubyte), ctypes.c_void_p)
ADDRESS_REPLY = ctypes.CFUNCTYPE(None, Ref, ctypes.c_uint32, ctypes.c_uint32, ctypes.c_int32,
                                 ctypes.c_char_p, ctypes.c_void_p, ctypes.c_uint32, ctypes.c_void_p)
# The daemon's replies, as its C API passes them; context is never used.
Browse = namedtuple("Browse", "ref flags interface code name regtype domain context")
Resolve = namedtuple("Resolve", "ref flags interface code fullname target port length txt context")
Address = namedtuple("Address", "ref flags interface code hostname sockaddr ttl context")
Target = namedtuple("Target", "interface name regtype domain")  # an instance seen on an interface
SIGNATURES = {
    "DNSServiceBrowse": (ctypes.c_int32, [ctypes.POINTER(Ref), ctypes.c_uint32, ctypes.c_uint32,
                                          ctypes.c_char_p, ctypes.c_char_p, BROWSE_REPLY, ctypes.c_void_p]),
    "DNSServiceResolve": (ctypes.c_int32, [ctypes.POINTER(Ref), ctypes.c_uint32, ctypes.c_uint32, ctypes.c_char_p,
                                           ctypes.c_char_p, ctypes.c_char_p, RESOLVE_REPLY, ctypes.c_void_p]),
    "DNSServiceGetAddrInfo": (ctypes.c_int32, [ctypes.POINTER(Ref), ctypes.c_uint32, ctypes.c_uint32, ctypes.c_uint32,
                                               ctypes.c_char_p, ADDRESS_REPLY, ctypes.c_void_p]),
    "DNSServiceRefSockFD": (ctypes.c_int, [Ref]),
    "DNSServiceProcessResult": (ctypes.c_int32, [Ref]),
    "DNSServiceRefDeallocate": (None, [Ref]),
}


def wanted(group, port, default_group, default_port):
    """Whether browsing goes through the system daemon: on Darwin, for the
    standard mDNS group and port; a custom endpoint keeps the multicast path."""
    return sys.platform.startswith("darwin") and (group, port) == (default_group, default_port)


def describe(code):
    return "%s (%d)" % (ERRORS.get(code, "error"), code)


class DaemonError(OSError):
    """The daemon refused or dropped an operation."""


class Daemon:
    """The DNS-SD C API: each start returns the operation's handle or raises
    DaemonError; the callbacks stay referenced for as long as their handles."""

    def __init__(self):
        self.lib = ctypes.CDLL(LIBRARY)
        for name, (result, arguments) in SIGNATURES.items():
            function = getattr(self.lib, name)
            function.restype, function.argtypes = result, arguments
        self.callbacks = {}

    def start(self, name, callback, *arguments):
        ref = Ref()
        code = getattr(self.lib, name)(ctypes.pointer(ref), *arguments, callback, None)
        if code:
            raise DaemonError(describe(code))
        self.callbacks[ref.value] = callback
        return ref.value

    def browse(self, callback):
        return self.start("DNSServiceBrowse", callback, 0, ALL_INTERFACES, REGTYPE, DOMAIN)

    def resolve(self, target, callback):
        return self.start("DNSServiceResolve", callback, 0, *target)

    def addresses(self, interface, host, callback):
        return self.start("DNSServiceGetAddrInfo", callback, 0, interface, PROTOCOLS, host)

    def descriptor(self, ref):
        return self.lib.DNSServiceRefSockFD(ref)

    def process(self, ref):
        """Deliver the reply waiting on the handle; only called when its descriptor is readable."""
        code = self.lib.DNSServiceProcessResult(ref)
        if code:
            raise DaemonError(describe(code))

    def stop(self, ref):
        self.lib.DNSServiceRefDeallocate(ref)
        self.callbacks.pop(ref, None)


def unpack(sockaddr):
    """The address in a BSD sockaddr, or None for other families."""
    if not sockaddr:
        return None
    size = ctypes.string_at(sockaddr, 1)[0] or SOCKADDR_MAX
    data = ctypes.string_at(sockaddr, min(size, SOCKADDR_MAX))
    if len(data) >= 8 and data[1] == socket.AF_INET:
        return socket.inet_ntop(socket.AF_INET, data[4:8])
    if len(data) >= 24 and data[1] == AF_INET6_DARWIN:
        return socket.inet_ntop(socket.AF_INET6, data[8:24])
    return None


def strings_of(txt, length):
    """The TXT strings the daemon handed over, or none when malformed."""
    if not txt or not length:
        return ()
    data = ctypes.string_at(txt, length)
    try:
        return txt_value(data, 0, length)
    except Malformed:
        return ()


class Feed:
    """Drives a Browser from the daemon's replies. error holds why the feed
    stopped, once it has; everything is closed by then."""

    def __init__(self, browser, clock):
        self.browser, self.clock = browser, clock
        self.daemon = Daemon()
        self.error = None
        self.handles = {}     # ref -> kind
        self.resolving = {}   # (interface, instance) -> ref
        self.looking = {}     # (interface, host) -> ref
        self.present = {}     # instance -> {interface: host once resolved, else None}
        self.addresses = {}   # host -> {value: {interface: expiry}}
        self.handles[self.daemon.browse(BROWSE_REPLY(self.found))] = "browse"

    # --- handles ---------------------------------------------------------------

    def start_resolve(self, key, target):
        reply = RESOLVE_REPLY(lambda *arguments: self.guard(self.resolved, key, Resolve(*arguments)))
        self.resolving[key] = ref = self.daemon.resolve(target, reply)
        self.handles[ref] = "resolve"

    def start_lookup(self, key, target):
        reply = ADDRESS_REPLY(lambda *arguments: self.guard(self.addressed, key, Address(*arguments)))
        self.looking[key] = ref = self.daemon.addresses(key[0], target, reply)
        self.handles[ref] = "address"

    def stop(self, ref):
        if ref in self.handles:
            del self.handles[ref]
            self.daemon.stop(ref)

    def stop_resolve(self, key):
        self.stop(self.resolving.pop(key, None))

    def stop_lookup(self, key):
        self.stop(self.looking.pop(key, None))

    def descriptors(self):
        return {self.daemon.descriptor(ref): ref for ref in list(self.handles)}

    def pump(self, ready):
        """Deliver the replies on the ready descriptors; a daemon failure closes everything."""
        for fd, ref in self.descriptors().items():
            if fd in ready and ref in self.handles:
                try:
                    self.daemon.process(ref)
                except DaemonError as error:
                    self.fail(error)

    def guard(self, action, *arguments):
        """Nothing may escape a C callback: ctypes would swallow it and the
        browse would look healthy; a failure here ends the feed instead."""
        try:
            action(*arguments)
        except Exception as error:  # noqa: BLE001  anything, by design
            self.fail(error)

    def trouble(self, code):
        """A reply's error: the daemon's own failures end the feed; one
        record's (no such record, timeout) end only its operation."""
        if code in SYSTEMIC:
            self.fail(DaemonError(describe(code)))

    def fail(self, error):
        self.error = str(error) or type(error).__name__
        self.close()

    def close(self):
        for ref in list(self.handles):
            self.stop(ref)
        self.resolving.clear()
        self.looking.clear()
        self.present.clear()
        self.addresses.clear()

    # --- browse ----------------------------------------------------------------

    def found(self, *arguments):
        self.guard(self.heard, Browse(*arguments))

    def heard(self, reply):
        if reply.code:
            self.fail(DaemonError(describe(reply.code)))
            return
        instance = "%s.%s" % (reply.name.decode("utf-8", "replace"), SERVICE)
        if reply.flags & FLAG_ADD:
            self.appeared(instance, Target(reply.interface, reply.name, reply.regtype, reply.domain))
        else:
            self.vanished(instance, reply.interface)

    def appeared(self, instance, target):
        if self.browser.is_own(instance):
            return
        self.present.setdefault(instance, {}).setdefault(target.interface, None)
        self.browser.service_record(instance, LIFETIME, self.clock(), None)
        key = (target.interface, instance)
        if key not in self.resolving and self.unresolved(key):
            self.start_resolve(key, target)
        self.prune()

    def unresolved(self, key):
        """The instance is still reported on the interface and has no host yet."""
        interface, instance = key
        places = self.present.get(instance, {})
        return interface in places and places[interface] is None

    def prune(self):
        """Instances the cache has already dropped need no native work."""
        for instance in [value for value in self.present if value not in self.browser.services]:
            self.forget(instance)

    def forget(self, instance):
        for interface, host in self.present.pop(instance, {}).items():
            self.stop_resolve((interface, instance))
            if host is not None:
                self.release(interface, host)

    def vanished(self, instance, interface):
        places = self.present.get(instance)
        if places is None:
            return
        host = places.pop(interface, None)
        self.stop_resolve((interface, instance))
        if host is not None:
            self.release(interface, host)
        if not places:
            del self.present[instance]
            self.browser.service_record(instance, 0, self.clock(), None)

    # --- resolve ---------------------------------------------------------------

    def resolved(self, key, reply):
        self.stop_resolve(key)
        if reply.code:
            self.trouble(reply.code)
            return
        if not self.unresolved(key):
            return  # gone meanwhile
        interface, instance = key
        host, now = reply.target.decode("utf-8", "replace").rstrip("."), self.clock()
        self.browser.service_record(instance, LIFETIME, now, (host, socket.ntohs(reply.port)))
        self.browser.text_record(instance, strings_of(reply.txt, reply.length), LIFETIME, now)
        self.present[instance][interface] = host
        if (interface, host) not in self.looking:
            self.start_lookup((interface, host), reply.target)

    # --- addresses -------------------------------------------------------------

    def addressed(self, key, reply):
        if reply.code:
            self.stop_lookup(key)
            self.trouble(reply.code)
            return
        value = unpack(reply.sockaddr)
        if value is not None:
            interface, host = key
            expiry = self.clock() + reply.ttl if reply.flags & FLAG_ADD else None
            self.set_address(host, value, interface, expiry)

    def set_address(self, host, value, interface, expiry):
        values = self.addresses.setdefault(host, {})
        places = values.setdefault(value, {})
        if expiry is None:
            places.pop(interface, None)
        else:
            places[interface] = expiry
        while len(values) > ADDRESSES_MAX:
            values.pop(next(iter(values)))
        self.publish(host, value)

    def publish(self, host, value):
        """The cache sees one record per address: alive until the last interface's expiry."""
        now = self.clock()
        places = self.addresses.get(host, {}).get(value, {})
        self.browser.address_record(host, value, max(max(places.values()) - now, 0) if places else 0, now)
        if not places:
            self.addresses.get(host, {}).pop(value, None)
            if not self.addresses.get(host):
                self.addresses.pop(host, None)

    def release(self, interface, host):
        """The interface's lookup of the host, once no instance there needs it."""
        if any(places.get(interface) == host for places in self.present.values()):
            return
        self.stop_lookup((interface, host))
        for value in list(self.addresses.get(host, {})):
            self.set_address(host, value, interface, None)
