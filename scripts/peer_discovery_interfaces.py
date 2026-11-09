#!/usr/bin/env python3
"""This host's interface addresses for discovery: by ioctl and /proc on
Linux, by ifconfig on macOS and other BSDs, and the default routes' source
addresses when neither names an eligible one. stdlib only."""

import ipaddress
import shutil
import socket
import struct
import subprocess
import sys

from peer_discovery_net import address, eligible

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


