"""Exercise the real macOS DNS-SD ABI using local-only daemon registrations.

This proves callbacks and lifecycle, not discovery between two physical Macs.
The synthetic address is registered locally and no connection is made to it.
"""

import ctypes as c
import select
import socket
import sys
import time
import unittest
import uuid
from unittest import mock

from discovery_test_support import engine


@unittest.skipUnless(sys.platform == "darwin", "requires the macOS Bonjour daemon")
class BonjourNativeAcceptance(unittest.TestCase):
    def setUp(self):
        self.engine = engine(self)
        self.lib = c.CDLL("/usr/lib/libSystem.B.dylib")
        self.refs, self.callbacks, self.errors = [], [], []
        self.addCleanup(self.close)
        self.bind("DNSServiceCreateConnection", c.c_int32, [c.POINTER(c.c_void_p)])
        self.bind("DNSServiceRefDeallocate", None, [c.c_void_p])
        self.bind("DNSServiceRefSockFD", c.c_int, [c.c_void_p])
        self.bind("DNSServiceProcessResult", c.c_int32, [c.c_void_p])
        self.record_callback = c.CFUNCTYPE(None, c.c_void_p, c.c_void_p, c.c_uint32,
                                          c.c_int32, c.c_void_p)
        self.service_callback = c.CFUNCTYPE(None, c.c_void_p, c.c_uint32, c.c_int32,
                                           c.c_char_p, c.c_char_p, c.c_char_p, c.c_void_p)
        self.bind("DNSServiceRegisterRecord", c.c_int32,
                  [c.c_void_p, c.POINTER(c.c_void_p), c.c_uint32, c.c_uint32, c.c_char_p,
                   c.c_uint16, c.c_uint16, c.c_uint16, c.c_void_p, c.c_uint32,
                   self.record_callback, c.c_void_p])
        self.bind("DNSServiceRegister", c.c_int32,
                  [c.POINTER(c.c_void_p), c.c_uint32, c.c_uint32, c.c_char_p, c.c_char_p,
                   c.c_char_p, c.c_char_p, c.c_uint16, c.c_uint16, c.c_void_p,
                   self.service_callback, c.c_void_p])

    def bind(self, name, result, arguments):
        function = getattr(self.lib, name)
        function.restype, function.argtypes = result, arguments

    def close(self):
        for ref in self.refs:
            self.lib.DNSServiceRefDeallocate(ref)
        self.refs.clear()

    def register(self):
        token = uuid.uuid4().hex[:16].encode()
        host = token + b".local."
        connection = c.c_void_p()
        self.assertEqual(self.lib.DNSServiceCreateConnection(c.byref(connection)), 0)
        self.refs.append(connection)
        callback = self.record_callback(lambda ref, record, flags, error, context:
                                        self.errors.append(error) if error else None)
        self.callbacks.append(callback)
        record = c.c_void_p()
        address = c.create_string_buffer(socket.inet_aton("192.0.2.164"))
        # Unique A record, IN class; LocalOnly prevents publishing on the LAN.
        self.assertEqual(self.lib.DNSServiceRegisterRecord(
            connection, c.byref(record), 0x20, 0xffffffff, host, 1, 1, 4,
            address, 60, callback, None), 0)
        service = c.c_void_p()
        callback = self.service_callback(lambda ref, flags, error, name, kind, domain, context:
                                         self.errors.append(error) if error else None)
        self.callbacks.append(callback)
        name = b"Bonjour ABI " + token
        txt = b"\x03v=1" + bytes([len(name) + 5]) + b"name=" + name
        buffer = c.create_string_buffer(txt)
        self.assertEqual(self.lib.DNSServiceRegister(
            c.byref(service), 0, 0xffffffff, token, b"_pairtask._tcp", b"local.",
            host, socket.htons(7445), len(txt), buffer, callback, None), 0)
        self.refs.append(service)
        return service, name.decode()

    def wait_for(self, session, predicate):
        deadline = time.monotonic() + 10
        while time.monotonic() < deadline:
            descriptors = {self.lib.DNSServiceRefSockFD(ref): ref for ref in self.refs}
            readable, _, _ = select.select(list(descriptors), [], [], 0)
            for fd in readable:
                self.assertEqual(self.lib.DNSServiceProcessResult(descriptors[fd]), 0)
            self.assertEqual(self.errors, [], "native registration failed asynchronously")
            session.step(0.05)
            rows = session.browser.invitations(time.monotonic())
            if predicate(rows):
                return rows
        self.fail("native Bonjour did not produce the expected rows within 10 seconds")

    def test_real_daemon_browse_resolve_address_and_remove(self):
        service, name = self.register()
        with mock.patch.object(self.engine, "Channel", side_effect=AssertionError("raw socket opened")):
            session = self.engine.Session(None)
            self.addCleanup(session.stop)
            session.update(False, True)
            rows = self.wait_for(session, lambda rows: any(row["name"] == name for row in rows))
            row, = [row for row in rows if row["name"] == name]
            self.assertEqual((row["host"], row["port"]), ("192.0.2.164", 7445))
            self.lib.DNSServiceRefDeallocate(service)
            self.refs.remove(service)
            self.wait_for(session, lambda rows: not any(row["name"] == name for row in rows))
            session.update(False, False)
            self.assertIsNone(session.browser)


if __name__ == "__main__":
    unittest.main()
