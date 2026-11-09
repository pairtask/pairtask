"""Invitation discovery over real mDNS records, without network sockets."""

import struct
import unittest

from discovery_test_support import SERVICE, engine, name, packet, record, service_records


def invitation_packet(display, port=7445):
    index, ttl = 1, 60
    records = service_records(index, "192.168.8.20", ttl)
    if port != 7445:
        instance = "node%d.%s" % (index, SERVICE)
        records[1] = record(instance, 33, struct.pack("!3H", 0, 0, port) + name("random%d.local." % index), ttl)
    instance = "node%d.%s" % (index, SERVICE)
    fields = [b"v=1", b"name=" + display]
    txt = b"".join(bytes([len(field)]) + field for field in fields)
    records.append(record(instance, 16, txt, ttl))
    return packet(records)


class DiscoveryInvitationAcceptance(unittest.TestCase):
    def setUp(self):
        self.discovery = engine(self)
        self.browser = self.discovery.Browser()

    def test_only_named_open_invitation_is_listed_and_keeps_its_real_port(self):
        ordinary = self.discovery.Advert(7445)
        ordinary.addresses = ["192.168.8.21"]
        self.browser.receive(ordinary.packet(60), 100.0)
        self.assertEqual(self.browser.invitations(100.0), [])

        self.browser.receive(invitation_packet("Office Mac".encode(), port=8445), 100.0)
        found = self.browser.invitations(101.0)
        self.assertEqual(len(found), 1)
        self.assertEqual({key: found[0][key] for key in ("name", "host", "port")},
                         {"name": "Office Mac", "host": "192.168.8.20", "port": 8445})
        self.assertIn("192.168.8.20:8445", self.browser.candidates(101.0))
        self.assertEqual(self.browser.invitations(221.0), [])

    def test_bad_txt_and_goodbye_cannot_leave_a_phantom_listing(self):
        self.browser.receive(invitation_packet(b"Office Mac"), 100.0)
        self.assertEqual(len(self.browser.invitations(101.0)), 1)
        goodbye = record(SERVICE, 12, name("node1." + SERVICE), 0)
        self.browser.receive(packet([goodbye]), 102.0)
        self.assertEqual(self.browser.invitations(102.0), [])

        for invalid in (b"\xff", b"a" * 64):
            with self.subTest(invalid=invalid):
                browser = self.discovery.Browser()
                browser.receive(invitation_packet(invalid), 100.0)
                self.assertEqual(browser.invitations(101.0), [])

    def test_invitation_packet_has_name_but_never_code_or_stable_key(self):
        advert = self.discovery.Advert(7445, invitation_name="Office Mac")
        advert.addresses = ["192.168.8.20"]
        wire = advert.packet(60)
        self.assertIn(b"name=Office Mac", wire)
        self.assertNotIn(b"sha256:", wire)
        self.assertNotIn(b"739-281", wire)
        self.browser.receive(wire, 100.0)
        self.assertEqual(self.browser.invitations(101.0)[0]["name"], "Office Mac")

    def test_replacing_an_invitation_under_the_same_name_rotates_its_instance(self):
        session = self.discovery.Session.__new__(self.discovery.Session)
        session.advert = self.discovery.Advert(7445)
        session.advert.addresses = ["192.168.8.20"]
        session.advertising = True
        session.adverts = session.named_since = 0
        session.named_key = None
        session.clock = lambda: 100.0
        goodbyes, scheduled = [], []
        session.send_advert = lambda ttl: goodbyes.append((ttl, session.advert.instance, session.advert.packet(ttl)))
        session.schedule_announcements = scheduled.append

        session.set_name("Office Mac", "first-invitation")
        old_instance = session.advert.instance
        session.set_name("Office Mac", "replacement-invitation")

        self.assertNotEqual(session.advert.instance, old_instance)
        self.assertEqual(goodbyes[-1][:2], (0, old_instance))
        self.assertIn(old_instance.split(".")[0].encode(), goodbyes[-1][2])
        self.assertIn(b"name=Office Mac", session.advert.packet(60))
        self.assertEqual(scheduled, [100.0, 100.0])


if __name__ == "__main__":
    unittest.main()
