"""Tests du module mitmproxy de la passerelle (gwfiles/gwproxy.py) : choix de la règle et filtre par domaine.

Lancés par « go test » (TestGwproxyPython) ; mitmproxy n'est pas nécessaire : il est remplacé par un module vide.
"""
import importlib.util
import json
import os
import sys
import tempfile
import types
import unittest

stub = types.ModuleType("mitmproxy")
stub.ctx, stub.http = types.SimpleNamespace(), types.SimpleNamespace(HTTPFlow=object)
sys.modules["mitmproxy"] = stub
spec = importlib.util.spec_from_file_location(
    "gwproxy", os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "gwfiles", "gwproxy.py"))
gwproxy = importlib.util.module_from_spec(spec)
spec.loader.exec_module(gwproxy)


def rules(*data):
    with tempfile.NamedTemporaryFile("w", suffix=".json", delete=False) as f:
        json.dump(list(data), f)
    try:
        return gwproxy.load_rules(f.name)
    finally:
        os.unlink(f.name)


class Match(unittest.TestCase):
    def setUp(self):
        self.rules = rules(
            {"module": "netlab", "target": "internet", "mode": "log", "sources": ["10.73.73.0/24"], "dest": "internet"},
            {"module": "netlab", "target": "intra", "mode": "intercept", "sources": ["10.73.73.10"], "dest": "10.75.75.0/24"},
        )

    def test_internet(self):
        self.assertEqual(gwproxy.match(self.rules, "10.73.73.20", "93.184.216.34")["target"], "internet")

    def test_internet_never_private(self):
        # « internet » ne couvre jamais une adresse privée : ni le LAN, ni un autre module
        for dst in ("192.168.1.10", "10.66.66.30", "172.16.0.1", "127.0.0.1"):
            self.assertIsNone(gwproxy.match(self.rules, "10.73.73.20", dst), dst)

    def test_module_destination(self):
        self.assertEqual(gwproxy.match(self.rules, "10.73.73.10", "10.75.75.5")["mode"], "intercept")
        self.assertIsNone(gwproxy.match(self.rules, "10.73.73.11", "10.75.75.5"))  # autre source

    def test_unknown_source_and_garbage(self):
        self.assertIsNone(gwproxy.match(self.rules, "10.99.0.1", "93.184.216.34"))
        self.assertIsNone(gwproxy.match(self.rules, "pas-une-ip", "93.184.216.34"))

    def test_unreadable_conf(self):
        self.assertEqual(gwproxy.load_rules("/nonexistent/proxy.json"), [])


class Allowed(unittest.TestCase):
    def test_not_filter(self):
        self.assertTrue(gwproxy.allowed({"mode": "log"}, "n-importe.quoi"))

    def test_allow_list(self):
        r = {"mode": "filter", "allow": ["*.debian.org", "example.com"]}
        self.assertTrue(gwproxy.allowed(r, "deb.debian.org"))
        self.assertTrue(gwproxy.allowed(r, "EXAMPLE.com."))  # casse et point final ignorés
        self.assertFalse(gwproxy.allowed(r, "debian.org.evil.com"))
        self.assertFalse(gwproxy.allowed(r, "evil.com"))
        self.assertFalse(gwproxy.allowed(r, ""))  # pas de SNI : refusé en liste blanche
        self.assertFalse(gwproxy.allowed(r, None))

    def test_deny_list(self):
        r = {"mode": "filter", "deny": ["*.tracker.net"]}
        self.assertFalse(gwproxy.allowed(r, "ads.tracker.net"))
        self.assertTrue(gwproxy.allowed(r, "example.com"))
        self.assertTrue(gwproxy.allowed(r, ""))  # liste noire : un client sans SNI passe (limite documentée)


if __name__ == "__main__":
    unittest.main()
