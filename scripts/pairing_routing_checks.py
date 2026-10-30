"""Live proof that paired workspaces carry messages to and from agents (#222).

Runs after the live link proof: the host side shares one agent with the
secured side, a minimal agent connects to the host broker at protocol 12,
the secured operator writes to it with `contact send --to`, and the agent
gets it at once, marked as from that workspace. Its acknowledgment and its
reply go back the same way: the reply shows in the secured operator's
contact page as from that agent, answering the message it was sent.
"""

import json
import socket
import time
import uuid

from pairing_live_checks import INSTANT, await_received, contact, page, require

AGENT = "proof-agent"
REMOTE_VERSION = 12


class Agent:
    """A minimal agent on a broker's agent socket: JSON lines, protocol 12."""

    def __init__(self, path, agent_id):
        self.sock = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        self.sock.settimeout(INSTANT)
        self.sock.connect(path)
        self.reader = self.sock.makefile("r", encoding="utf-8")
        self.send({"type": "hello", "agent_id": agent_id, "protocol_version": REMOTE_VERSION})
        welcome = self.next_of("welcome", INSTANT)
        require(welcome.get("protocol_version") == REMOTE_VERSION, f"agent not at protocol {REMOTE_VERSION}: {welcome}")

    def send(self, frame):
        self.sock.sendall((json.dumps(frame) + "\n").encode())

    def next_of(self, kind, timeout):
        """The next frame of this type within timeout seconds."""
        return self.await_frame(lambda f: f.get("type") == kind, time.monotonic() + timeout)

    def await_frame(self, wanted, deadline):
        """The next frame wanted accepts, read by the deadline; an error
        frame fails the proof, and other frames are passed over, never
        taken for the one wanted."""
        while True:
            remaining = deadline - time.monotonic()
            require(remaining > 0, "the agent's frame did not arrive in time")
            self.sock.settimeout(remaining)
            frame = self.read_frame()
            require(time.monotonic() <= deadline, "the agent's frame arrived too late")
            if wanted(frame):
                return frame

    def read_frame(self):
        line = self.reader.readline()
        require(line, "the broker closed the agent's connection")
        frame = json.loads(line)
        require(frame.get("type") != "error", f"the broker refused: {frame}")
        return frame

    def acknowledge(self, message_id):
        """Both receipt stages; the broker's confirmations are passed over."""
        for stage in ("adapter_received", "agent_acknowledged"):
            self.send({"type": "ack", "message_id": message_id, "stage": stage})

    def close(self):
        self.reader.close()
        self.sock.close()


def ack_of(message_id, stage):
    """Matches the broker's ack of this message at this stage."""
    return lambda f: f.get("type") == "ack" and (f.get("message_id"), f.get("stage")) == (message_id, stage)


def check_import(msg, sender_key, sent):
    """The agent's copy names the workspace it came from and its operator."""
    message_id, text = sent
    remote = msg.get("remote") or {}
    require(msg.get("origin") == "remote", f"{message_id} arrived with origin {msg.get('origin')!r}")
    require(remote.get("contact") == sender_key, f"{message_id} names another workspace: {remote}")
    require(msg.get("from") == "operator" and not remote.get("sender"), f"{message_id} names another sender: {msg.get('from')!r}")
    require((msg.get("payload") or {}).get("text") == text, f"{message_id} arrived with other text")


def await_acknowledged(run, key, message_id, deadline):
    """The sent message lists the agent as having acknowledged it."""
    while time.monotonic() < deadline:
        rows = [m for m in page(run, key).get("messages", []) if m.get("direction") == "out" and m.get("id") == message_id]
        if rows and rows[0].get("agent_acks") == [AGENT]:
            return
        time.sleep(0.2)
    raise ValueError(f"{AGENT}'s acknowledgment of {message_id} did not come back in time")


def to_agent(sides, agent):
    """The secured operator writes to the host's agent; the agent's copy."""
    (secured, host_key), (_, secured_key) = sides
    ping = f"route-{uuid.uuid4().hex[:12]}"
    started = time.monotonic()
    contact(secured, "send", host_key, f"ping {ping}", "--to", AGENT, "--message-id", ping)
    msg = agent.next_of("msg", INSTANT)
    elapsed = time.monotonic() - started
    check_import(msg, secured_key, (ping, f"ping {ping}"))
    require(elapsed <= INSTANT, f"{ping} took {elapsed:.1f}s to reach {AGENT}")
    print(f"PASS: operator to a paired workspace's agent in {elapsed:.1f}s.", flush=True)
    return ping, msg


def reply_back(sides, agent, ping, msg):
    """The agent acknowledges and replies; within INSTANT seconds the reply
    shows in the secured operator's page, from the agent and answering the
    ping, and the ping shows the agent's acknowledgment."""
    (secured, host_key), _ = sides
    started = time.monotonic()
    deadline = started + INSTANT
    agent.acknowledge(msg["message_id"])
    reply = f"reply-{uuid.uuid4().hex[:12]}"
    agent.send({"type": "msg", "to": "operator", "message_id": reply, "in_reply_to": msg["message_id"],
                "payload": {"text": f"pong {reply}"}, "durable": True, "ack_requested": True})
    agent.await_frame(ack_of(reply, "accepted"), deadline)
    await_received(secured, host_key, (reply, f"pong {reply}"), timeout=deadline - time.monotonic())
    row = next(m for m in page(secured, host_key)["messages"] if m.get("direction") == "in" and m.get("id") == reply)
    require(row.get("from") == AGENT and row.get("reply_id") == ping, f"reply arrived without its agent or parent: {row}")
    await_acknowledged(secured, host_key, ping, deadline)
    elapsed = time.monotonic() - started
    require(elapsed <= INSTANT, f"the reply and acknowledgment took {elapsed:.1f}s, over {INSTANT}s")
    print(f"PASS: {AGENT}'s acknowledgment and reply back to the operator in {elapsed:.1f}s.", flush=True)


def prove_routing(sides, agent_socket):
    """sides as for prove_live, secured first; agent_socket is the host
    broker's agent socket."""
    _, (host, secured_key) = sides
    contact(host, "share", secured_key, AGENT)
    agent = Agent(agent_socket, AGENT)
    try:
        ping, msg = to_agent(sides, agent)
        reply_back(sides, agent, ping, msg)
    finally:
        agent.close()
    print("PASS: a paired workspace's operator and agent talk both ways over the live link.", flush=True)
