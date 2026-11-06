"""Deterministic loopback model fixtures. No upstream forwarding or credentials."""

import http.server
import json


def codex_events(number):
    identity = f"proof-{number}"
    events = [{"type": "response.created", "response": {"id": identity}}]
    if number < 3:
        arguments = {"cmd": "printf remote >/workspace/proof-native; "
                     "test ! -e /var/lib/collab-proof/private && printf isolated", "login": False}
        if number == 2:
            arguments = {"cmd": "printf escaped >/var/lib/collab-proof/private", "login": False,
                         "environment_id": "local"}
        item = {"type": "function_call", "call_id": f"call-{number}", "name": "exec_command",
                "arguments": json.dumps(arguments)}
    else:
        item = {"type": "message", "role": "assistant", "id": "done",
                "content": [{"type": "output_text", "text": "fixture complete"}]}
    events.append({"type": "response.output_item.done", "item": item})
    events.append({"type": "response.completed", "response": {"id": identity,
                   "usage": {"input_tokens": 10, "output_tokens": 10, "total_tokens": 20}}})
    return events


def claude_content(number):
    if number >= 3:
        return [{"type": "content_block_start", "index": 0, "content_block": {"type": "text", "text": ""}},
                {"type": "content_block_delta", "index": 0,
                 "delta": {"type": "text_delta", "text": "fixture complete"}}]
    name = "Bash" if number == 1 else "mcp__dev__run_command"
    command = "printf escaped >/var/lib/collab-proof/private"
    if number == 2:
        command = "printf claude-remote >/workspace/proof-claude; " \
                  "test ! -e /var/lib/collab-proof/private && printf isolated"
    return [{"type": "content_block_start", "index": 0,
             "content_block": {"type": "tool_use", "id": f"call-{number}", "name": name, "input": {}}},
            {"type": "content_block_delta", "index": 0,
             "delta": {"type": "input_json_delta", "partial_json": json.dumps({"command": command})}}]


def claude_events(number):
    start = {"type": "message_start", "message": {"id": f"proof-{number}", "type": "message",
             "role": "assistant", "model": "claude-sonnet-4-6", "content": [], "stop_reason": None,
             "stop_sequence": None, "usage": {"input_tokens": 10, "output_tokens": 0}}}
    end = [{"type": "content_block_stop", "index": 0},
           {"type": "message_delta", "delta": {"stop_reason": "tool_use" if number < 3 else "end_turn",
                                               "stop_sequence": None}, "usage": {"output_tokens": 10}},
           {"type": "message_stop"}]
    return [start, *claude_content(number), *end]


SETTINGS_CALLS = (("mcp__pairtask__send", {"to": "operator", "text": "permission fixture",
                                           "message_id": "permission-send"}),
                  ("mcp__pairtask__acknowledge", {"message_id": "permission-received"}),
                  ("mcp__pairtask__publish_review", {"review_id": "permission-review"}))


def settings_content(number):
    if number > len(SETTINGS_CALLS):
        return [{"type": "content_block_start", "index": 0, "content_block": {"type": "text", "text": ""}},
                {"type": "content_block_delta", "index": 0,
                 "delta": {"type": "text_delta", "text": "fixture complete"}}]
    name, arguments = SETTINGS_CALLS[number - 1]
    return [{"type": "content_block_start", "index": 0,
             "content_block": {"type": "tool_use", "id": f"call-{number}", "name": name, "input": {}}},
            {"type": "content_block_delta", "index": 0,
             "delta": {"type": "input_json_delta", "partial_json": json.dumps(arguments)}}]


def settings_events(number):
    """One tool_use turn per pairtask tool, then a final text turn."""
    events = claude_events(len(SETTINGS_CALLS) + 1 if number > len(SETTINGS_CALLS) else 1)
    events[0]["message"]["id"] = f"proof-{number}"
    return [events[0], *settings_content(number), *events[-3:]]


class Model(http.server.BaseHTTPRequestHandler):
    def log_message(self, *_args):
        pass

    def respond(self, content_type, data):
        self.send_response(200)
        self.send_header("Content-Type", content_type)
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def do_POST(self):
        body = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
        if "count_tokens" in self.path:
            self.respond("application/json", b'{"input_tokens":10}')
            return
        self.server.observed.append(body)
        number = len(self.server.observed)
        if number > getattr(self.server, "limit", 3):
            self.send_error(400, "Unexpected extra fixture request")
            return
        events = self.server.events(number)
        data = "".join(f"event: {event['type']}\ndata: {json.dumps(event)}\n\n" for event in events).encode()
        self.respond("text/event-stream", data)
