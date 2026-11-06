#!/usr/bin/env python3
"""Offline proof fixture: a stdio MCP server named pairtask that only logs tool calls."""

import json
import sys


TOOLS = ("send", "acknowledge", "publish_review")


def describe(name):
    return {"name": name, "description": f"Offline fixture stand-in for pairtask {name}",
            "inputSchema": {"type": "object", "additionalProperties": True}}


def call(params, log):
    with open(log, "a") as handle:
        handle.write(json.dumps({"name": params["name"], "arguments": params.get("arguments", {})}) + "\n")
    return {"content": [{"type": "text", "text": f"fixture {params['name']} recorded"}], "isError": False}


def result(request, log):
    method = request.get("method")
    if method == "initialize":
        version = request.get("params", {}).get("protocolVersion", "2024-11-05")
        return {"protocolVersion": version, "capabilities": {"tools": {}},
                "serverInfo": {"name": "pairtask", "version": "proof"}}
    if method == "tools/list":
        return {"tools": [describe(name) for name in TOOLS]}
    if method == "tools/call":
        return call(request["params"], log)
    return {}


def main(log):
    for line in sys.stdin:
        request = json.loads(line)
        if "id" in request:
            print(json.dumps({"jsonrpc": "2.0", "id": request["id"], "result": result(request, log)}), flush=True)


if __name__ == "__main__":
    main(sys.argv[1])
