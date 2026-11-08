import struct
import unittest

from discovery_test_support import SERVICE, engine, name, packet, record, service_records


class DiscoveryWireAcceptance(unittest.TestCase):
    def setUp(self):
        self.browser = engine(self).Browser()

    def test_standard_service_resolves_across_separate_dns_packets(self):
        records = service_records()
        self.browser.receive(packet(records[:2]), 100.0)
        self.assertEqual(self.browser.candidates(100.0), [])
        self.browser.receive(packet(records[2:]), 101.0)
        self.assertIn("192.168.8.20:7445", self.browser.candidates(101.0))

    def test_goodbye_removes_service_before_address_ttl(self):
        self.browser.receive(packet(service_records()), 100.0)
        self.assertTrue(self.browser.candidates(100.0))
        goodbye = record(SERVICE, 12, name("node1." + SERVICE), 0)
        self.browser.receive(packet([goodbye]), 101.0)
        self.assertEqual(self.browser.candidates(101.0), [])

    def test_cache_and_ttl_are_bounded(self):
        for index in range(1, 41):
            records = service_records(index, "192.168.8.%d" % index, 999999)
            self.browser.receive(packet(records), 100.0)
        candidates = self.browser.candidates(101.0)
        self.assertGreater(len(candidates), 0)
        self.assertLessEqual(len(candidates), 8)
        self.assertEqual(self.browser.candidates(221.0), [])

    def test_loopback_and_link_local_are_not_candidates(self):
        for address in ["127.0.0.1", "169.254.1.2", "::1", "fe80::1"]:
            with self.subTest(address=address):
                browser = engine(self).Browser()
                browser.receive(packet(service_records(address=address)), 100.0)
                self.assertEqual(browser.candidates(101.0), [])

    def test_malformed_dns_cannot_populate_cache(self):
        header = struct.pack("!6H", 0, 0x8400, 0, 1, 0, 0)
        self_pointer = header + b"\xc0\x0c" + struct.pack("!HHIH", 1, 1, 60, 4) + b"\x01\x02\x03\x04"
        over_records = struct.pack("!6H", 0, 0x8400, 0, 65, 0, 0)
        for data in [b"short", self_pointer, over_records, b"x" * 9001]:
            with self.subTest(length=len(data)):
                self.browser.receive(data, 100.0)
                self.assertEqual(self.browser.candidates(100.0), [])

    def test_ipv6_ula_is_a_bracketed_endpoint(self):
        self.browser.receive(packet(service_records(address="fd7a:115c:a1e0::123")), 100.0)
        self.assertIn("[fd7a:115c:a1e0::123]:7445", self.browser.candidates(101.0))

    def test_configured_cache_capacity_evicts_oldest_service(self):
        browser = engine(self).Browser(max_entries=2)
        for index in range(1, 4):
            browser.receive(packet(service_records(index, "192.168.8.%d" % index)), 100.0 + index)
        candidates = browser.candidates(104.0)
        self.assertEqual(len(candidates), 2)
        self.assertNotIn("192.168.8.1:7445", candidates)
