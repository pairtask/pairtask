"""Controllable DNS-SD C API with real readiness descriptors, no network traffic."""

import ctypes
import os
import socket
from collections import deque, namedtuple


Resolved = namedtuple("Resolved", "name port txt error", defaults=("Office Mac", 7445, None, 0))
Address = namedtuple("Address", "host ttl add error", defaults=("192.168.1.164", 60, True, 0))


def number(value):
    return value.value if hasattr(value, "value") else value


class Function:
    """Allow a binding to set ctypes signatures on these C API stand-ins."""

    def __init__(self, call):
        self.call = call

    def __call__(self, *args):
        return self.call(*args)


class BonjourAPI:
    def __init__(self):
        self.operations = {}
        self.closed = []
        self.next_id = 1
        self.start_error = 0
        for name, implementation in {
                "DNSServiceBrowse": self.browse,
                "DNSServiceResolve": self.resolve,
                "DNSServiceGetAddrInfo": self.addresses,
                "DNSServiceRefSockFD": self.descriptor,
                "DNSServiceProcessResult": self.process,
                "DNSServiceRefDeallocate": self.deallocate}.items():
            setattr(self, name, Function(implementation))

    def allocate(self, output, operation):
        if self.start_error:
            return self.start_error
        ref = self.next_id
        self.next_id += 1
        reader, writer = os.pipe()
        self.operations[ref] = dict(operation, reader=reader, writer=writer, events=deque())
        ctypes.cast(output, ctypes.POINTER(ctypes.c_void_p))[0] = ref
        return 0

    def browse(self, *arguments):
        output, flags, interface, regtype, domain, callback, context = arguments
        return self.allocate(output, dict(kind="browse", callback=callback, context=context,
                                          interface=number(interface), regtype=regtype, domain=domain))

    def resolve(self, *arguments):
        output, flags, interface, name, regtype, domain, callback, context = arguments
        return self.allocate(output, dict(kind="resolve", callback=callback, context=context,
                                          interface=number(interface), name=name, regtype=regtype, domain=domain))

    def addresses(self, *arguments):
        output, flags, interface, protocol, host, callback, context = arguments
        return self.allocate(output, dict(kind="address", callback=callback, context=context,
                                          interface=number(interface), host=host))

    def descriptor(self, ref):
        return self.operations[number(ref)]["reader"]

    def process(self, ref):
        ref = number(ref)
        operation = self.operations[ref]
        os.read(operation["reader"], 1)
        event = operation["events"].popleft()
        event(operation["callback"], ref, operation["context"])
        return 0

    def deallocate(self, ref):
        ref = number(ref)
        operation = self.operations.pop(ref)
        self.closed.append(ref)
        os.close(operation["reader"])
        os.close(operation["writer"])

    def close(self):
        for ref in list(self.operations):
            self.deallocate(ref)

    def refs(self, kind):
        return [ref for ref, operation in self.operations.items() if operation["kind"] == kind]

    def queue(self, ref, event):
        operation = self.operations[ref]
        operation["events"].append(event)
        os.write(operation["writer"], b"x")

    def service(self, name=b"0123456789abcdef", interface=14, add=True, error=0):
        ref, = self.refs("browse")
        self.queue(ref, lambda callback, ref, context:
                   callback(ref, 2 if add else 0, interface, error,
                            name, b"_pairtask._tcp.", b"local.", context))

    @staticmethod
    def pointer(callback, index, buffer):
        # Use the binding's declared pointer type, including unsigned-char TXT.
        kinds = getattr(callback, "argtypes", None)
        return ctypes.cast(buffer, kinds[index] if kinds else ctypes.c_void_p)

    def resolved(self, ref, **changes):
        result = Resolved(**changes)
        operation = self.operations[ref]
        name = result.name.encode()
        raw = result.txt if result.txt is not None else b"\x03v=1" + bytes([len(name) + 5]) + b"name=" + name
        buffer = ctypes.create_string_buffer(raw)

        def event(callback, ref, context):
            callback(ref, 0, operation["interface"], result.error,
                     operation["name"] + b"._pairtask._tcp.local.", b"random.local.",
                     socket.htons(result.port), len(raw), self.pointer(callback, 8, buffer), context)
        self.queue(ref, event)

    def address(self, ref, **changes):
        result = Address(**changes)
        operation = self.operations[ref]
        if ":" in result.host:
            packed = bytes([28, 30]) + b"\0" * 6 + socket.inet_pton(socket.AF_INET6, result.host) + b"\0" * 4
        else:
            packed = bytes([16, 2]) + b"\0" * 2 + socket.inet_aton(result.host) + b"\0" * 8
        buffer = ctypes.create_string_buffer(packed)

        def event(callback, ref, context):
            callback(ref, 2 if result.add else 0, operation["interface"], result.error,
                     operation["host"], self.pointer(callback, 5, buffer), result.ttl, context)
        self.queue(ref, event)
