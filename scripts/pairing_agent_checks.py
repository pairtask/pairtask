"""Live proof that agents of paired workspaces write first (#222).

Runs with the host's agent from the routing proof still connected. The
secured operator shares one of its agents with the host; that agent runs in
the secured deployment's workspace, a short script on its broker socket.
Each way, an agent writes first to the other workspace's operator and to
its agent, and gets an answer correlated to its own message. Every round
trip is timed on one clock, from the send to the answer, within INSTANT;
starting the secured agent is never counted.
"""

import json
import threading
import time
import uuid

from pairing_live_checks import INSTANT, await_received, contact, page, require
from pairing_routing_checks import AGENT, ack_of

SECURED_AGENT = "proof-secured"
STARTUP = INSTANT * 3  # the secured agent's script starts and connects

# The secured side's agent. Arguments: mode, agent, workspace, to, the ID
# and text it writes, the ID and text it expects, and seconds to wait.
# "first" writes first (to, in workspace) and waits for the expected
# message; "answer" waits for it and answers it. The expected message has
# that exact text, and answers that ID, or nothing when the ID is "-". It
# prints the message it got and the round trip from its send, or from its
# receipt to the acceptance of its answer.
SECURED_AGENT_SCRIPT = r'''
import json, socket, sys, time
mode, agent_id, workspace, to, message_id, text, expect_id, expect_text, wait = sys.argv[1:10]
deadline = time.monotonic() + float(wait)
sock = socket.socket(socket.AF_UNIX)
sock.connect("/mnt/collab-ipc/broker.sock")
reader = sock.makefile("r", encoding="utf-8")

def send(frame):
    sock.sendall((json.dumps(frame) + "\n").encode())

def frame(wanted):
    while True:
        remaining = deadline - time.monotonic()
        if remaining <= 0:
            raise SystemExit("no frame in time")
        sock.settimeout(remaining)
        line = reader.readline()
        if not line or time.monotonic() > deadline:
            raise SystemExit("the connection closed or the frame came too late")
        got = json.loads(line)
        if got.get("type") == "error":
            raise SystemExit("refused: " + line)
        if wanted(got):
            return got

def write(extra):
    send(dict(extra, type="msg", message_id=message_id, payload={"text": text}, durable=True, ack_requested=True))
    frame(lambda f: f.get("type") == "ack" and f.get("message_id") == message_id and f.get("stage") == "accepted")

def expected(f):
    if f.get("type") != "msg" or f.get("origin") != "remote":
        return False
    if (f.get("payload") or {}).get("text") != expect_text:
        return False
    if expect_id != "-":
        return f.get("in_reply_to") == expect_id
    return not f.get("in_reply_to")

send({"type": "hello", "agent_id": agent_id, "protocol_version": 13})
frame(lambda f: f.get("type") == "welcome")
started = time.monotonic()
if mode == "first":
    write({"to": to, "workspace": workspace})
    got = frame(expected)
else:
    got = frame(expected)
    started = time.monotonic()
    write({"to": got["from"], "in_reply_to": got["message_id"]})
elapsed = time.monotonic() - started
for stage in ("adapter_received", "agent_acknowledged"):
    send({"type": "ack", "message_id": got["message_id"], "stage": stage})
print(json.dumps({"got": got, "elapsed": elapsed}))
'''


class SecuredAgent:
    """The secured side's agent: one script run at a time, in a thread."""

    def __init__(self, deployment):
        self.deployment, self.result, self.thread = deployment, None, None

    def start(self, *args):
        self.thread = threading.Thread(target=self.call, args=(args,), daemon=True)
        self.thread.start()

    def call(self, args):
        self.result = self.deployment.secured_agent(SECURED_AGENT_SCRIPT, *args, str(STARTUP + INSTANT))

    def await_online(self):
        """Its script connected: what it is sent from now on isn't kept
        waiting for it to start."""
        deadline = time.monotonic() + STARTUP
        while time.monotonic() < deadline:
            if self.deployment.agent_online(SECURED_AGENT):
                return
            time.sleep(0.5)
        raise ValueError(f"{SECURED_AGENT} did not connect within {STARTUP}s")

    def message(self):
        """The message the script got, once it ended within its round trip."""
        self.thread.join(STARTUP + INSTANT + 30)
        require(not self.thread.is_alive(), f"{SECURED_AGENT} did not finish")
        result = self.result
        require(result.returncode == 0, f"{SECURED_AGENT} failed: {result.stdout} {result.stderr}")
        out = json.loads(result.stdout.strip().splitlines()[-1])
        require(out["elapsed"] <= INSTANT, f"{SECURED_AGENT}'s round trip took {out['elapsed']:.1f}s, over {INSTANT}s")
        return out["got"]


def within(started, what):
    """The time since started, at most INSTANT; what names the exchange."""
    elapsed = time.monotonic() - started
    require(elapsed <= INSTANT, f"{what} took {elapsed:.1f}s, over {INSTANT}s")
    return elapsed


def check_message(msg, origin, text):
    """A message from the other workspace: origin is (from, contact,
    sender there), and its text exactly as written."""
    sender, contact_key, remote_sender = origin
    remote = msg.get("remote") or {}
    require(msg.get("origin") == "remote" and msg.get("from") == sender, f"not from {sender} there: {msg}")
    require(remote.get("contact") == contact_key, f"from another workspace: {remote}")
    require(remote.get("sender", "") == remote_sender, f"from another sender there: {remote}")
    require((msg.get("payload") or {}).get("text") == text, f"other text: {msg.get('payload')}")


def ids():
    return f"first-{uuid.uuid4().hex[:12]}", f"answer-{uuid.uuid4().hex[:12]}"


def secured_first(sides, agent, secured):
    """The secured agent writes first to the host's agent, which answers;
    the answer reaches it with in_reply_to its own message."""
    (_, host_key), (_, secured_key) = sides
    first, answer = ids()
    secured.start("first", SECURED_AGENT, host_key, AGENT, first, f"hello {first}", first, f"hi {answer}")
    msg = agent.await_frame(lambda f: f.get("type") == "msg" and f.get("from") == SECURED_AGENT, time.monotonic() + STARTUP + INSTANT)
    check_message(msg, (SECURED_AGENT, secured_key, SECURED_AGENT), f"hello {first}")
    require(not msg.get("in_reply_to"), f"a first message with a parent: {msg}")
    agent.acknowledge(msg["message_id"])
    answered = time.monotonic()
    agent.send({"type": "msg", "to": SECURED_AGENT, "in_reply_to": msg["message_id"], "message_id": answer,
                "payload": {"text": f"hi {answer}"}, "durable": True, "ack_requested": True})
    agent.await_frame(ack_of(answer, "accepted"), answered + INSTANT)
    got = secured.message()
    elapsed = within(answered, f"the answer to {first}")
    check_message(got, (AGENT, host_key, AGENT), f"hi {answer}")
    require(got.get("in_reply_to") == first, f"the answer lost its parent: {got}")
    print(f"PASS: secured's agent wrote first to the host's agent; the answer reached it in {elapsed:.1f}s.", flush=True)


def host_first(sides, agent, secured):
    """The host's agent writes first to the secured agent, once connected,
    which answers; the answer reaches it with in_reply_to its message."""
    (_, host_key), (_, secured_key) = sides
    first, answer = ids()
    secured.start("answer", SECURED_AGENT, "-", "-", answer, f"hi {answer}", "-", f"hello {first}")
    secured.await_online()
    started = time.monotonic()
    agent.send({"type": "msg", "to": SECURED_AGENT, "workspace": secured_key, "message_id": first,
                "payload": {"text": f"hello {first}"}, "durable": True, "ack_requested": True})
    agent.await_frame(ack_of(first, "accepted"), started + INSTANT)
    msg = agent.await_frame(lambda f: f.get("type") == "msg" and f.get("in_reply_to") == first, started + INSTANT)
    elapsed = within(started, f"the answer to {first}")
    check_message(msg, (SECURED_AGENT, secured_key, SECURED_AGENT), f"hi {answer}")
    agent.acknowledge(msg["message_id"])
    got = secured.message()
    check_message(got, (AGENT, host_key, AGENT), f"hello {first}")
    require(not got.get("in_reply_to"), f"a first message with a parent: {got}")
    print(f"PASS: the host's agent wrote first to secured's agent; its answer came back in {elapsed:.1f}s.", flush=True)


def secured_to_operator(sides, secured):
    """The secured agent writes first to the host's operator, who writes
    back to it; the round trip is timed by the agent."""
    (_, host_key), (host, secured_key) = sides
    first, answer = ids()
    secured.start("first", SECURED_AGENT, host_key, "operator", first, f"hello {first}", "-", f"hi {answer}")
    await_received(host, secured_key, (first, f"hello {first}"), timeout=STARTUP + INSTANT)
    row = next(m for m in page(host, secured_key)["messages"] if m.get("direction") == "in" and m.get("id") == first)
    require(row.get("from") == SECURED_AGENT and not row.get("to"), f"wrong sender or recipients: {row}")
    answered = time.monotonic()
    contact(host, "send", secured_key, f"hi {answer}", "--to", SECURED_AGENT, "--message-id", answer)
    got = secured.message()
    elapsed = within(answered, f"the operator's answer {answer}")
    check_message(got, ("operator", host_key, ""), f"hi {answer}")
    print(f"PASS: secured's agent wrote first to the host's operator, whose answer reached it in {elapsed:.1f}s.", flush=True)


def host_to_operator(sides, agent):
    """The host's agent writes first to the secured operator, at once."""
    (secured, host_key), (_, secured_key) = sides
    first = ids()[0]
    started = time.monotonic()
    agent.send({"type": "msg", "to": "operator", "workspace": secured_key, "message_id": first,
                "payload": {"text": f"hello {first}"}, "durable": True, "ack_requested": True})
    agent.await_frame(ack_of(first, "accepted"), started + INSTANT)
    await_received(secured, host_key, (first, f"hello {first}"), timeout=started + INSTANT - time.monotonic())
    row = next(m for m in page(secured, host_key)["messages"] if m.get("direction") == "in" and m.get("id") == first)
    require(row.get("from") == AGENT, f"wrong sender: {row}")
    require(not row.get("reply_id") and not row.get("to"), f"a parent or recipients on a message to the operator: {row}")
    elapsed = within(started, first)
    print(f"PASS: the host's agent wrote first to secured's operator in {elapsed:.1f}s.", flush=True)


def prove_agents(sides, deployment):
    """The hook prove_routing takes. deployment runs a Python script with
    its arguments as the secured workspace's agent user (secured_agent),
    and says whether an agent is connected there (agent_online)."""
    (secured, host_key), _ = sides
    contact(secured, "share", host_key, SECURED_AGENT)
    secured_agent = SecuredAgent(deployment)

    def across(agent):
        host_to_operator(sides, agent)
        secured_to_operator(sides, secured_agent)
        secured_first(sides, agent, secured_agent)
        host_first(sides, agent, secured_agent)
        print("PASS: agents of paired workspaces write first to operators and agents, and get answers, both ways.", flush=True)

    return across
