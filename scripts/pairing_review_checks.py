"""Live proof of review requests between paired workspaces (#235).

Runs with the host's agent from the routing proof still connected, and the
secured agent of the agent proof. Each side's operator lets the other ask
its agent for reviews (auto). Each way, an agent asks the other side's
agent for a review pinned to two commits; the reviewer declines one and
completes another with a comment verdict, publishing nothing; and the
outcome reaches the agent that asked, answering its request, within
INSTANT of the request's send. Each reviewer finds its request by the
ID the asker gave it, and each outcome must answer that request, from that
reviewer, on that change, with what the reviewer decided.
"""

import json
import time
import uuid

from pairing_agent_checks import SECURED_AGENT, STARTUP, SecuredAgent
from pairing_live_checks import INSTANT, contact, require
from pairing_routing_checks import AGENT, ack_of

BASE, HEAD = "1" * 40, "2" * 40
REPOSITORY, CHANGE = "github.com/pairtask/pairtask", "235"
VERDICT = {"outcome": "comment", "summary": "proof review: nothing to add", "findings": []}
DECLINED = "proof: declined"
FINAL = {"decline": "declined", "complete": "completed"}

# The secured side's agent for reviews. Arguments: mode, agent, workspace,
# to, the request's ID, what to do or expect, and seconds to wait.
# "review" waits for the notice of the review request with that ID there
# and declines or completes it, printing the notice and the review; "ask"
# asks to's agent in workspace for a review and waits for the outcome it
# expects, printing it and the time since the send.
SECURED_REVIEW_SCRIPT = r'''
import json, socket, sys, time, uuid
mode, agent_id, workspace, to, ask_id, what, wait = sys.argv[1:8]
BASE, HEAD = "1" * 40, "2" * 40
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

def call(kind, **fields):
    request = str(uuid.uuid4())
    send(dict(fields, type=kind, request_id=request))
    return frame(lambda f: f.get("request_id") == request)

def ack(msg):
    for stage in ("adapter_received", "agent_acknowledged"):
        send({"type": "ack", "message_id": msg["message_id"], "stage": stage})

send({"type": "hello", "agent_id": agent_id, "protocol_version": 15})
frame(lambda f: f.get("type") == "welcome")
if mode == "review":
    while True:
        notice = frame(lambda f: f.get("type") == "msg" and f.get("origin") == "remote" and f.get("review_id"))
        ack(notice)
        review = call("review_get", review_id=notice["review_id"]).get("review") or {}
        if review.get("remote_request") == ask_id:
            break
    rid = notice["review_id"]
    if what == "decline":
        call("review_decline", review_id=rid, review_reason="proof: declined")
    else:
        call("review_prepare", review_id=rid, revisions={"base": BASE, "head": HEAD})
        call("review_accept", review_id=rid)
        call("review_submit", review_id=rid, attempt=1,
             verdict={"outcome": "comment", "summary": "proof review: nothing to add", "findings": []})
    print(json.dumps({"got": notice, "review": review}))
else:
    started = time.monotonic()
    send({"type": "msg", "to": to, "workspace": workspace, "message_id": ask_id, "durable": True, "ack_requested": True,
          "review_ask": {"forge": "github", "repository": "github.com/pairtask/pairtask", "change": "235", "base": BASE, "head": HEAD}})
    frame(lambda f: f.get("type") == "ack" and f.get("message_id") == ask_id and f.get("stage") == "accepted")
    while True:
        got = frame(lambda f: f.get("type") == "msg" and (f.get("review_outcome") or {}).get("ask_id") == ask_id)
        ack(got)
        if got["review_outcome"]["state"] == what:
            break
    print(json.dumps({"got": got, "elapsed": time.monotonic() - started}))
'''


class SecuredReviewer(SecuredAgent):
    """The secured agent running the review script."""

    def call(self, args):
        self.result = self.deployment.secured_agent(SECURED_REVIEW_SCRIPT, *args, str(STARTUP + INSTANT))

    def reviewed(self):
        """The notice and review the script handled in review mode; the
        asker times the exchange, not the reviewer."""
        out = self.output()
        return out["got"], out["review"]


def check_outcome(msg, origin, ask_id, final):
    """An outcome from the other workspace's reviewer, answering the request,
    on its change, in the state expected with what the reviewer decided, and
    nothing published; origin is (reviewer, contact)."""
    check_answer(msg, origin, ask_id)
    outcome = msg.get("review_outcome") or {}
    require(outcome.get("reviewer") == origin[0], f"another reviewer's outcome: {outcome}")
    require((outcome.get("repository"), outcome.get("change")) == (REPOSITORY, CHANGE), f"another change's outcome: {outcome}")
    require(outcome.get("state") == final, f"outcome {outcome.get('state')}, not {final}")
    require(not outcome.get("intent") and not outcome.get("url"), f"published although nothing may be: {outcome}")
    check_decided(outcome, final)


def check_answer(msg, origin, ask_id):
    """A message from that reviewer of that workspace, answering the
    request with that ID."""
    reviewer, contact_key = origin
    remote = msg.get("remote") or {}
    require(msg.get("origin") == "remote" and msg.get("from") == reviewer, f"not from {reviewer} there: {msg}")
    require(remote.get("contact") == contact_key and remote.get("sender") == reviewer, f"from another workspace or sender: {remote}")
    require(msg.get("in_reply_to") == ask_id, f"answers {msg.get('in_reply_to')}, not {ask_id}")
    require((msg.get("review_outcome") or {}).get("ask_id") == ask_id, f"another request's outcome: {msg}")


def check_decided(outcome, final):
    """What the reviewer decided: the verdict given, or the reason it
    declined."""
    if final == "completed":
        decided = (outcome.get("verdict"), outcome.get("summary"), outcome.get("attempt"))
        require(decided == (VERDICT["outcome"], VERDICT["summary"], 1), f"not the verdict given: {outcome}")
    else:
        require(outcome.get("reason") == DECLINED, f"not the reason given: {outcome}")


def check_review(notice, review, ask_id, contact_key):
    """The review a reviewer handled: the request with that ID from the
    asking workspace, pinned to the asked revisions of the asked change."""
    spec = review.get("spec") or {}
    require((notice.get("remote") or {}).get("contact") == contact_key, f"the request named another workspace: {notice}")
    require(review.get("remote_request") == ask_id, f"another request's review: {review}")
    require((spec.get("repository"), spec.get("change")) == (REPOSITORY, CHANGE), f"another change's review: {spec}")
    require(review.get("pinned") == {"base": BASE, "head": HEAD}, f"pinned to other revisions: {review.get('pinned')}")


def host_asks(sides, agent, secured, decision):
    """The host's agent asks the secured agent for a review, which declines
    or completes it; the outcome answers the request within INSTANT."""
    (_, host_key), (_, secured_key) = sides
    ask = f"ask-{uuid.uuid4().hex[:12]}"
    secured.start("review", SECURED_AGENT, "-", "-", ask, decision)
    secured.await_online()
    started = time.monotonic()
    agent.send({"type": "msg", "to": SECURED_AGENT, "workspace": secured_key, "message_id": ask, "durable": True, "ack_requested": True,
                "review_ask": {"forge": "github", "repository": REPOSITORY, "change": CHANGE, "base": BASE, "head": HEAD}})
    agent.await_frame(ack_of(ask, "accepted"), started + INSTANT)
    final = FINAL[decision]
    msg = agent.await_frame(outcome_of(ask, final), started + INSTANT)
    elapsed = time.monotonic() - started
    check_outcome(msg, (SECURED_AGENT, secured_key), ask, final)
    agent.acknowledge(msg["message_id"])
    check_review(*secured.reviewed(), ask, host_key)
    require(elapsed <= INSTANT, f"{ask} took {elapsed:.1f}s to be {final}, over {INSTANT}s")
    print(f"PASS: the host's agent asked secured's agent for a review; {final} came back in {elapsed:.1f}s.", flush=True)


def outcome_of(ask, final):
    """Matches the outcome of the request in that state."""
    def wanted(f):
        outcome = f.get("review_outcome") or {}
        return f.get("type") == "msg" and outcome.get("ask_id") == ask and outcome.get("state") == final
    return wanted


def host_call(agent, kind, fields, deadline):
    """One review call by the host's agent, and its answer."""
    request = str(uuid.uuid4())
    agent.send(dict(fields, type=kind, request_id=request))
    got = agent.await_frame(lambda f: f.get("request_id") == request, deadline)
    require(got.get("type") != "error", f"{kind} refused: {got}")
    return got


def host_reviews(agent, ask, decision, deadline):
    """The host's agent finds the review request with that ID, and declines
    or completes it; it returns the notice and the review."""
    while True:
        notice = agent.await_frame(lambda f: f.get("type") == "msg" and f.get("origin") == "remote" and f.get("review_id"), deadline)
        agent.acknowledge(notice["message_id"])
        rid = notice["review_id"]
        review = host_call(agent, "review_get", {"review_id": rid}, deadline).get("review") or {}
        if review.get("remote_request") == ask:
            break
    steps = [("review_decline", {"review_reason": DECLINED})]
    if decision == "complete":
        steps = [("review_prepare", {"revisions": {"base": BASE, "head": HEAD}}), ("review_accept", {}),
                 ("review_submit", {"attempt": 1, "verdict": VERDICT})]
    for kind, fields in steps:
        host_call(agent, kind, dict(fields, review_id=rid), deadline)
    return notice, review


def secured_asks(sides, agent, secured, decision):
    """The secured agent asks the host's agent for a review, which declines
    or completes it; the outcome answers the request, timed by the asker."""
    (_, host_key), (_, secured_key) = sides
    ask = f"ask-{uuid.uuid4().hex[:12]}"
    final = FINAL[decision]
    secured.start("ask", SECURED_AGENT, host_key, AGENT, ask, final)
    notice, review = host_reviews(agent, ask, decision, time.monotonic() + STARTUP + INSTANT)
    check_review(notice, review, ask, secured_key)
    msg = secured.message()
    check_outcome(msg, (AGENT, host_key), ask, final)
    print(f"PASS: secured's agent asked the host's agent for a review; {final} came back.", flush=True)


def prove_reviews(sides, deployment):
    """A hook for prove_routing: each side lets the other ask its agent for
    reviews, then the four requests run."""
    (secured, host_key), (host, secured_key) = sides
    contact(secured, "reviews", host_key, SECURED_AGENT, "--mode", "auto")
    contact(host, "reviews", secured_key, AGENT, "--mode", "auto")
    reviewer = SecuredReviewer(deployment)

    def across(agent):
        for decision in ("decline", "complete"):
            host_asks(sides, agent, reviewer, decision)
            secured_asks(sides, agent, reviewer, decision)
        print("PASS: agents of paired workspaces ask each other for reviews, and the outcomes come back, both ways.", flush=True)

    return across

