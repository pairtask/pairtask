"""Live proof of publishing the pairing relay (#222) on a disposable deployment.

Each stage changes pairing_host_port, applies, and checks Incus itself: the
project's proxy restriction, connector's exact forwarder, the state, that
secured and workspace devices are unchanged, and which host ports accept
connections. A taken port must fail its apply and succeed once released;
turning the route off must remove connector and restore the restriction.
The redemption through the published port is the identity proof; a TCP
connection only shows a listener.
"""

import json
import socket
import subprocess
import time

import sandbox_pairing

PROXY = "restricted.devices.proxy"
BOUNDARY = ("secured", "workspace")


def require(condition, message):
    if not condition:
        raise ValueError(message)


def host_address():
    """This host's non-loopback IPv4 address: where another machine connects."""
    with socket.socket(socket.AF_INET, socket.SOCK_DGRAM) as probe:
        probe.connect(("192.0.2.1", 9))  # picks the outbound address; sends nothing
        address = probe.getsockname()[0]
    require(not address.startswith("127."), f"no non-loopback host address: {address}")
    return address


def free_ports(count):
    ports = set()
    while len(ports) < count:
        with socket.socket() as probe:
            probe.bind(("0.0.0.0", 0))
            ports.add(probe.getsockname()[1])
    return sorted(ports)


def accepts(address, port, timeout=1):
    try:
        socket.create_connection((address, port), timeout=timeout).close()
        return True
    except OSError:
        return False


def await_accepts(address, port, timeout=30):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        if accepts(address, port):
            return
        time.sleep(0.5)
    raise ValueError(f"{address}:{port} never accepted connections")


class Publication:
    """One disposable deployment's tofu directory and its Incus project."""

    def __init__(self, remote, project, directory, tofu):
        self.remote, self.project = remote.rstrip(":") + ":", project
        self.directory, self.tofu = directory, tofu
        self.address = host_address()

    def query(self, path):
        return json.loads(subprocess.check_output(["incus", "query", self.remote + path]))

    def restriction(self):
        return self.query(f"/1.0/projects/{self.project}")["config"].get(PROXY)

    def instances(self):
        return {item["name"] for item in self.query(f"/1.0/instances?project={self.project}&recursion=1")}

    def devices(self, name):
        return self.query(f"/1.0/instances/{name}?project={self.project}")["devices"]

    def boundary(self):
        return {name: self.query(f"/1.0/instances/{name}?project={self.project}")["expanded_devices"]
                for name in BOUNDARY}

    def set(self, **values):
        path = self.directory / "sandbox.auto.tfvars.json"
        variables = json.loads(path.read_text())
        variables.update(values)
        path.write_text(json.dumps(variables))

    def apply(self):
        return subprocess.run(self.tofu + ["apply", "-auto-approve", "-input=false"]).returncode

    def stop(self, *names):
        for name in names:
            subprocess.run(["incus", "--project", self.project, "stop", self.remote + name, "--force"], check=True)


def forwarder(port):
    return {"type": "proxy", "listen": f"tcp:0.0.0.0:{port}", "connect": "tcp:127.0.0.1:7445",
            "bind": "host", "nat": "false"}


def require_stage(publication, port, baseline):
    """The applied stage, from Incus and from the state."""
    restriction = publication.restriction()
    require(restriction == "allow", f"port {port}: the project's proxy restriction is {restriction}")
    devices = publication.devices("connector")
    expected = {"pairing-relay": forwarder(port)} if port else {}
    require(devices == expected, f"port {port}: connector's devices are {devices}")
    applied = sandbox_pairing.published_port(publication.directory, publication.project)
    require(applied == port, f"port {port}: the state publishes {applied}")
    require(publication.boundary() == baseline, f"port {port}: secured's or workspace's devices changed")


def require_listening(publication, port, used):
    address = publication.address
    if port:
        await_accepts(address, port)
    for other in used - {port}:
        require(not accepts(address, other), f"{address}:{other} still accepts after moving to {port}")


def prove_publishing(publication, redeem):
    """Publish on one port, move it, unpublish, publish again; then a taken
    port. redeem(destination) runs a real redemption and returns its code;
    returns the codes used and every port published."""
    baseline = publication.boundary()
    first, second, third, taken = free_ports(4)
    used, codes = set(), []
    for port in (first, second, 0, third):
        publication.set(pairing_host_port=port)
        require(publication.apply() == 0, f"publishing on port {port} failed")
        require_stage(publication, port, baseline)
        if port:
            used.add(port)
        require_listening(publication, port, used)
        if port == first:
            codes.append(redeem(f"{publication.address}:{port}"))
        print(f"PASS: port {port or 'none'} applied: restriction, forwarder, state, boundary and listeners.", flush=True)
    prove_collision(publication, taken, baseline, used)
    return codes, used


def prove_collision(publication, port, baseline, used):
    blocker = socket.socket()
    try:
        blocker.bind(("0.0.0.0", port))
        blocker.listen()
        publication.set(pairing_host_port=port)
        require(publication.apply() != 0, f"publishing on taken port {port} reported success")
        variables = json.loads((publication.directory / "sandbox.auto.tfvars.json").read_text())
        require(variables["pairing_host_port"] == port, "the failed apply lost the requested port")
    finally:
        blocker.close()
    print(f"PASS: publishing on taken port {port} failed and kept the request.", flush=True)
    require(publication.apply() == 0, f"publishing on port {port} failed after it was released")
    require_stage(publication, port, baseline)
    used.add(port)
    require_listening(publication, port, used)
    print(f"PASS: the retry published port {port} once it was free.", flush=True)


def prove_off(publication, used):
    """Turning the route off removes connector, its forwarder and the allowance."""
    publication.stop("connector", "secured")  # the route's preflight requires both stopped
    publication.set(pairing_route=False)
    require(publication.apply() == 0, "turning the route off failed")
    restriction = publication.restriction()
    require(restriction == "block", f"after off the project's proxy restriction is {restriction}")
    require("connector" not in publication.instances(), "connector survived turning the route off")
    applied = sandbox_pairing.published_port(publication.directory, publication.project)
    require(applied is None, f"after off the state still publishes {applied}")
    for port in used:
        require(not accepts(publication.address, port), f"{publication.address}:{port} still accepts after off")
    print("PASS: off removed connector and its forwarder, closed every port and restored block.", flush=True)
