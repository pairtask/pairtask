"""The mDNS wire format peer_discovery uses (#284): a strictly bounded
parser and a minimal encoder for _pairtask._tcp.local, stdlib only.

Packets are at most PACKET_MAX bytes with at most RECORD_MAX entries,
names at most NAME_MAX bytes with at most JUMPS_MAX compression pointers,
each pointing backwards, and no TTL is believed beyond TTL_MAX. parse()
drops anything else and never raises.
"""

from collections import namedtuple
import ipaddress
import struct

SERVICE = "_pairtask._tcp.local"
PACKET_MAX, RECORD_MAX, JUMPS_MAX, NAME_MAX = 9000, 64, 8, 255
TTL_MAX = 120          # seconds any record is believed, whatever it says
A, PTR, TXT, AAAA, SRV, ANY = 1, 12, 16, 28, 33, 255
CLASS_IN, FLUSH = 1, 0x8000
RESPONSE = 0x8400      # QR and AA: an authoritative answer
POINTER = 0xC0

Record = namedtuple("Record", "name kind ttl value")
Message = namedtuple("Message", "response questions records")
# What encode_record writes: flush marks it unique to this responder (RFC 6762 10.2).
Entry = namedtuple("Entry", "name kind data flush", defaults=(True,))


class Malformed(ValueError):
    """A packet outside the bounds; dropped, never raised to a caller."""


def require(condition, reason):
    if not condition:
        raise Malformed(reason)


class NameReader:
    """One name's labels from offset, following compression pointers only
    backwards, at most JUMPS_MAX of them, NAME_MAX bytes in all."""

    def __init__(self, data, offset):
        self.data, self.offset, self.limit = data, offset, offset
        self.labels, self.length, self.jumps, self.end = [], 1, 0, None

    def read(self):
        """The lowercased name and the offset after it in the packet."""
        while self.step():
            pass
        return ".".join(self.labels), self.after()

    def step(self):
        """Read one pointer or label; False at the root label."""
        size = self.data[self.offset]
        if size & POINTER == POINTER:
            self.jump(size)
            return True
        require(not size & POINTER, "reserved label type")
        self.offset += 1
        if size == 0:
            return False
        self.label(size)
        return True

    def jump(self, size):
        target = ((size & 0x3F) << 8) | self.data[self.offset + 1]
        self.jumps += 1
        require(self.jumps <= JUMPS_MAX, "too many compression pointers")
        require(target < self.limit, "compression pointer does not point backwards")
        if self.end is None:
            self.end = self.offset + 2
        self.offset = self.limit = target

    def label(self, size):
        label = self.data[self.offset:self.offset + size]
        self.length += size + 1
        require(len(label) == size, "label past the packet")
        require(self.length <= NAME_MAX, "name too long")
        self.labels.append(label.decode("utf-8", "replace").lower())
        self.offset += size

    def after(self):
        return self.offset if self.end is None else self.end


def read_name(data, offset):
    """The lowercased name at offset and the offset after it."""
    return NameReader(data, offset).read()


def exact_name(data, start, end):
    """A name that fills exactly the record data from start to end."""
    name, after = read_name(data, start)
    require(after == end, "name does not fill its record")
    return name


def srv_value(data, start, end):
    require(end - start >= 7, "short SRV")
    return exact_name(data, start + 6, end), struct.unpack_from("!H", data, start + 4)[0]


def ipv4_value(data, start, end):
    require(end - start == 4, "A is 4 bytes")
    return str(ipaddress.IPv4Address(data[start:end]))


def ipv6_value(data, start, end):
    require(end - start == 16, "AAAA is 16 bytes")
    return str(ipaddress.IPv6Address(data[start:end]))


def txt_value(data, start, end):
    """The TXT record's strings (RFC 1035 3.3.14), each at most 255 bytes,
    as given; what they mean is the reader's business."""
    strings, offset = [], start
    while offset < end:
        size = data[offset]
        offset += 1
        require(offset + size <= end, "TXT string past its record")
        strings.append(bytes(data[offset:offset + size]))
        offset += size
    return tuple(strings)


# The record types discovery reads, by how their data is read.
VALUES = {PTR: exact_name, SRV: srv_value, TXT: txt_value, A: ipv4_value, AAAA: ipv6_value}


def record_value(data, kind, start, end):
    """The part of a record discovery reads, or None for the others."""
    reader = VALUES.get(kind)
    return reader(data, start, end) if reader else None


def read_record(data, offset):
    name, offset = read_name(data, offset)
    kind, cls, ttl, size = struct.unpack_from("!HHIH", data, offset)
    start = offset + 10
    end = start + size
    require(end <= len(data), "record past the packet")
    value = record_value(data, kind, start, end) if cls & 0x7FFF == CLASS_IN else None
    return Record(name, kind, min(ttl, TTL_MAX), value), end


def read_questions(data, offset, count):
    asked = []
    for _ in range(count):
        name, offset = read_name(data, offset)
        asked.append((name, struct.unpack_from("!HH", data, offset)[0]))
        offset += 4
    return asked, offset


def read_records(data, offset, count):
    records = []
    for _ in range(count):
        record, offset = read_record(data, offset)
        records.append(record)
    return records


def decode(data):
    require(12 <= len(data) <= PACKET_MAX, "packet size")
    _, flags, questions, answers, authority, additional = struct.unpack_from("!6H", data)
    count = answers + authority + additional
    require(questions + count <= RECORD_MAX, "too many records")
    asked, offset = read_questions(data, 12, questions)
    return Message(bool(flags & 0x8000), asked, read_records(data, offset, count))


def parse(data):
    """The DNS message in data, or None when it is malformed or over a bound."""
    try:
        return decode(bytes(data))
    except (ValueError, IndexError, struct.error, TypeError):
        return None


def encode_name(name):
    labels = [label.encode("utf-8") for label in name.rstrip(".").split(".")]
    if not all(0 < len(label) < 64 for label in labels) or sum(len(l) + 1 for l in labels) + 1 > NAME_MAX:
        raise ValueError("not an encodable DNS name: %r" % name)
    return b"".join(bytes([len(label)]) + label for label in labels) + b"\0"


def encode_record(entry, ttl):
    """One record from an Entry, with this TTL (0: goodbye)."""
    cls = CLASS_IN | (FLUSH if entry.flush else 0)
    return encode_name(entry.name) + struct.pack("!HHIH", entry.kind, cls, ttl, len(entry.data)) + entry.data


def response(records):
    return struct.pack("!6H", 0, RESPONSE, 0, len(records), 0, 0) + b"".join(records)


def query():
    """One question: which instances of the service are there."""
    return struct.pack("!6H", 0, 0, 1, 0, 0, 0) + encode_name(SERVICE) + struct.pack("!HH", PTR, CLASS_IN)
