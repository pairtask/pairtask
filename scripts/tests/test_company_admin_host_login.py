"""The operator's browser callback is received on the host that opened it."""
import base64
import hashlib
from pathlib import Path
import runpy
import unittest
from unittest.mock import patch
from urllib.parse import parse_qs, urlencode, urlsplit
from urllib.request import urlopen


SCRIPTS = Path(__file__).resolve().parents[1]


class CompanyAdminHostLogin(unittest.TestCase):
    def test_browser_callback_matches_the_secured_exchange(self):
        login = runpy.run_path(str(SCRIPTS / "sandbox-pairbook-login.py"))
        challenge = []

        def browser(start):
            parsed = urlsplit(start)
            query = parse_qs(parsed.query)
            challenge.append(query["challenge"][0])
            callback = urlencode({"state": query["state"][0], "code": "one-time-code"})
            with urlopen("http://127.0.0.1:{}/done?{}".format(query["port"][0], callback), timeout=3):
                pass

        with patch("webbrowser.open", browser):
            exchange = login["browser_exchange"]("https://directory.example")
        expected = hashlib.sha256(exchange["verifier"].encode()).digest()
        self.assertEqual(challenge, [base64.urlsafe_b64encode(expected).rstrip(b"=").decode()])
        self.assertEqual(exchange["code"], "one-time-code")
        self.assertGreater(exchange["port"], 0)
