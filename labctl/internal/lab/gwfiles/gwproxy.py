"""Module mitmproxy de la passerelle core-gw (installé par labctl, chargé par gwproxy.service).

Le pare-feu redirige vers mitmproxy (mode transparent) le HTTP et le HTTPS des connexions « via: proxy » du
module.yaml. Pour chaque connexion, ce module retrouve la règle (source, destination) dans
/etc/gwdns/proxy.json (généré par labctl) et applique son mode :

  log        HTTPS non déchiffré (passage direct) ; journal : source, destination, nom TLS (SNI), requêtes HTTP
  filter     comme log, et seuls les domaines autorisés passent (allow), ou tous sauf les refusés (deny)
  intercept  HTTPS déchiffré avec l'autorité d'interception : requêtes et réponses complètes dans mitmweb

Une connexion qui ne correspond à aucune règle est coupée. Le journal (une ligne JSON par événement) est lu par
labctl pour la page Trafic de la GUI : /var/log/gwproxy/flows.jsonl.
"""
import fnmatch
import ipaddress
import json
import os
import time

from mitmproxy import ctx, http

CONF = "/etc/gwdns/proxy.json"
LOG = "/var/log/gwproxy/flows.jsonl"
MAX_LOG = 20 * 1024 * 1024  # puis rotation (.1)
MAX_FLOWS = 2000  # échanges gardés en mémoire pour mitmweb


def load_rules(path=CONF):
    try:
        with open(path) as f:
            data = json.load(f)
    except (OSError, ValueError):
        return []
    rules = []
    for r in data:
        r["_src"] = [ipaddress.ip_network(s, strict=False) for s in r.get("sources", [])]
        r["_dst"] = None if r.get("dest") == "internet" else ipaddress.ip_network(r["dest"], strict=False)
        rules.append(r)
    return rules


def match(rules, client, server):
    """Règle de la connexion client -> server (adresses IP), ou None."""
    try:
        c, s = ipaddress.ip_address(client), ipaddress.ip_address(server)
    except ValueError:
        return None
    for r in rules:
        if not any(c in n for n in r["_src"]):
            continue
        if r["_dst"] is None:
            if s.version == 4 and s.is_global:
                return r
        elif s in r["_dst"]:
            return r
    return None


def allowed(rule, host):
    """Le domaine passe-t-il le filtre de la règle ?"""
    if rule.get("mode") != "filter":
        return True
    host = (host or "").lower().rstrip(".")
    hit = lambda pats: any(fnmatch.fnmatch(host, p.lower()) for p in pats)
    if rule.get("allow"):
        return bool(host) and hit(rule["allow"])
    return not hit(rule.get("deny", []))


class GwProxy:
    def __init__(self):
        self.rules = []
        self.blocked = set()  # connexions client refusées (identifiant mitmproxy)

    def running(self):
        self.rules = load_rules()
        os.makedirs(os.path.dirname(LOG), exist_ok=True)
        ctx.log.info(f"gwproxy : {len(self.rules)} règle(s)")

    def log(self, **e):
        e["time"] = time.strftime("%Y-%m-%dT%H:%M:%S%z")
        try:
            if os.path.exists(LOG) and os.path.getsize(LOG) > MAX_LOG:
                os.replace(LOG, LOG + ".1")
            with open(LOG, "a") as f:
                f.write(json.dumps(e, ensure_ascii=False) + "\n")
        except OSError as err:
            ctx.log.warn(f"gwproxy : journal impossible : {err}")

    # TLS : décision avant toute connexion vers le serveur (connection_strategy=lazy)
    def tls_clienthello(self, data):
        client = data.context.client.peername[0]
        server = data.context.server.address[0] if data.context.server.address else ""
        sni = data.client_hello.sni or ""
        rule = match(self.rules, client, server)
        base = dict(kind="tls", src=client, dst=server, host=sni)
        if rule is None:
            self.blocked.add(data.context.client.id)
            data.ignore_connection = True
            self.log(**base, action="refusé", why="aucune règle")
            return
        base.update(module=rule["module"], target=rule["target"], mode=rule["mode"])
        if not allowed(rule, sni):
            self.blocked.add(data.context.client.id)
            data.ignore_connection = True
            self.log(**base, action="refusé", why="filtre")
            return
        if rule["mode"] != "intercept":
            data.ignore_connection = True  # passage direct, non déchiffré
            self.log(**base, action="passé")
        else:
            self.log(**base, action="intercepté")

    def server_connect(self, data):
        # Connexion refusée au ClientHello : pas de connexion vers le serveur, le client est coupé
        if data.client.id in self.blocked:
            data.server.error = "refusé par le proxy de la passerelle"

    def client_disconnected(self, client):
        self.blocked.discard(client.id)

    # HTTP (en clair, ou déchiffré en intercept)
    def request(self, flow: http.HTTPFlow):
        client = flow.client_conn.peername[0]
        server = flow.server_conn.address[0] if flow.server_conn.address else ""
        rule = match(self.rules, client, server)
        host = flow.request.pretty_host
        if rule is None or not allowed(rule, host):
            why = "aucune règle" if rule is None else "filtre"
            flow.response = http.Response.make(
                403, f"Refusé par le proxy de la passerelle ({why}) : {host}\n", {"Content-Type": "text/plain; charset=utf-8"})
            flow.metadata["gw"] = dict(rule=rule, action="refusé", why=why)
            return
        flow.metadata["gw"] = dict(rule=rule, action="intercepté" if flow.client_conn.tls_established else "passé")

    def response(self, flow: http.HTTPFlow):
        self.log_flow(flow)
        self.trim()

    def error(self, flow: http.HTTPFlow):
        self.log_flow(flow, error=str(flow.error))

    def log_flow(self, flow, error=""):
        gw = flow.metadata.get("gw", {})
        rule = gw.get("rule") or {}
        e = dict(kind="http", src=flow.client_conn.peername[0],
                 dst=flow.server_conn.address[0] if flow.server_conn.address else "",
                 host=flow.request.pretty_host, method=flow.request.method, url=flow.request.pretty_url,
                 module=rule.get("module", ""), target=rule.get("target", ""), mode=rule.get("mode", ""),
                 action=gw.get("action", "passé"))
        if gw.get("why"):
            e["why"] = gw["why"]
        if flow.response:
            e["status"] = flow.response.status_code
            e["size"] = len(flow.response.raw_content or b"")
        if error:
            e["error"] = error
        self.log(**e)

    def trim(self):
        """Garde les MAX_FLOWS derniers échanges dans mitmweb (mémoire de la passerelle)."""
        view = ctx.master.addons.get("view")
        if view is not None and len(view) > MAX_FLOWS:
            view.remove(list(view)[: len(view) - MAX_FLOWS])


addons = [GwProxy()]
