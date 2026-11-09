"""Addresses and endpoints for the host discovery helper (#284): the IPs a
machine could dial, HOST:PORT spelling, and bounded de-duplication."""
import ipaddress

CANDIDATES_MAX = 8     # endpoints in one submission
INVENTORY_MAX = 256    # tailnet endpoints remembered between submissions


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
