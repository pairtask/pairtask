#!/usr/bin/env python3
"""Receive the admin browser callback on the operator's host and exchange it inside the Box."""
import argparse
import base64
import hashlib
from http.server import BaseHTTPRequestHandler, HTTPServer
import json
from pathlib import Path
import secrets
import subprocess
import sys
from threading import Thread
import time
from urllib.parse import parse_qs, urlencode, urlsplit
import webbrowser


def origin(value):
    address = urlsplit(value)
    if address.scheme != "https" or not address.hostname or address.username is not None:
        raise argparse.ArgumentTypeError("give the Pairbook as https://HOST")
    result = "https://" + address.netloc
    if value not in (result, result + "/"):
        raise argparse.ArgumentTypeError("give the Pairbook as https://HOST")
    return result


class Callback(BaseHTTPRequestHandler):
    def setup(self):
        self.request.settimeout(3)
        super().setup()

    def do_GET(self):
        url = urlsplit(self.path)
        query = parse_qs(url.query)
        if url.path != "/done" or query.get("state") != [self.server.state]:
            self.send_error(400)
            return
        codes = query.get("code", [])
        if len(codes) != 1 or not codes[0]:
            self.send_error(400)
            return
        self.server.code = codes[0]
        self.send_response(200)
        self.send_header("Content-Type", "text/plain; charset=utf-8")
        self.send_header("Cache-Control", "no-store")
        self.end_headers()
        self.wfile.write(b"You can close this tab and return to your terminal.")

    def log_message(self, *args):
        # The callback query contains the one-time authorization code.
        pass


def browser_exchange(pairbook):
    verifier = secrets.token_urlsafe(48)
    challenge = base64.urlsafe_b64encode(hashlib.sha256(verifier.encode()).digest()).rstrip(b"=").decode()
    with HTTPServer(("127.0.0.1", 0), Callback) as server:
        server.code = None
        server.state = secrets.token_urlsafe(24)
        port = server.server_port
        url = pairbook + "/admin/cli?" + urlencode(dict(port=port, state=server.state, challenge=challenge))
        print("Sign in at " + url, file=sys.stderr, flush=True)
        Thread(target=webbrowser.open, args=(url,), daemon=True).start()
        deadline = time.monotonic() + 300
        while server.code is None:
            server.timeout = deadline - time.monotonic()
            if server.timeout <= 0:
                raise TimeoutError("no sign-in came back in time")
            server.handle_request()
        return {"port": port, "code": server.code, "verifier": verifier}


def main(argv=None):
    parser = argparse.ArgumentParser(prog="pairtask space admin login")
    parser.add_argument("pairbook", type=origin)
    parser.add_argument("--json", action="store_true")
    args = parser.parse_args(argv)
    try:
        exchange = browser_exchange(args.pairbook)
        command = [sys.executable, str(Path(__file__).with_name("sandbox-session.py")), "secured",
                   "pairtask", "operator", "space", "admin", "login-from-host", args.pairbook]
        if args.json:
            command.append("--json")
        return subprocess.run(command, input=json.dumps(exchange).encode()).returncode
    except OSError as error:
        print("pairtask space admin login: " + str(error), file=sys.stderr)
        return 1


if __name__ == "__main__":
    sys.exit(main())
