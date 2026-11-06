package coreconf

import (
	"encoding/json"
	"strings"
	"testing"

	"labs/labctl/internal/module"
)

func input() Input {
	return Input{
		RootDomain: "omedo.lab",
		Upstream:   "192.168.122.1",
		Attached:   []string{"demo", "pentest"},
		Modules: []Module{
			{
				Name: "demo", Subnet: "10.77.77", Domain: "demo.client.omedo.lab",
				Hosts: map[string]int{"srv": 10, "dc01": 40}, MACs: map[string]string{"srv": "52:54:00:77:77:10", "dc01": "52:54:00:aa:bb:cc"},
			},
			{
				Name: "dormant", Subnet: "10.88.88", Domain: "dormant.omedo.lab", // réseau non branché
				Hosts: map[string]int{"x": 10}, MACs: map[string]string{"x": "52:54:00:88:88:10"},
			},
			{
				Name: "pentest", Subnet: "10.66.66", Domain: "pentest-lab.omedo.lab",
				Hosts:      map[string]int{"web": 30, "kali": 10, "msf2": 20, "dnsonly": 50},
				MACs:       map[string]string{"kali": "52:54:00:66:66:10", "msf2": "52:54:00:66:66:20", "web": "52:54:00:66:66:30"},
				NoInternet: []string{"msf2"},
				Aliases:    map[string]string{"juice": "web", "dvwa": "web", "ext": "srv.demo.client.omedo.lab"},
				Links:      []string{"demo", "dormant"},
			},
		},
	}
}

func render(t *testing.T, in Input) Files {
	t.Helper()
	f, err := Render(in)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func has(t *testing.T, f Files, file, line string) {
	t.Helper()
	if !strings.Contains("\n"+f[file], "\n"+line+"\n") {
		t.Errorf("%s : ligne attendue absente : %q\n%s", file, line, f[file])
	}
}

func hasNot(t *testing.T, f Files, file, line string) {
	t.Helper()
	if strings.Contains("\n"+f[file], "\n"+line+"\n") {
		t.Errorf("%s : ligne inattendue : %q", file, line)
	}
}

func TestFilesAndPorts(t *testing.T) {
	f := render(t, input())
	// Seuls les modules branchés ont une configuration ; ports dans l'ordre des modules
	if f["gwdns/modules"] != "10.77.77.0/24 5300 5301 demo demo.client.omedo.lab\n10.66.66.0/24 5302 5303 pentest pentest-lab.omedo.lab\n" {
		t.Errorf("modules :\n%s", f["gwdns/modules"])
	}
	for _, name := range []string{"gwdns/demo-open.conf", "gwdns/pentest-closed.conf", "networkd/50-gw-pentest.network"} {
		if _, ok := f[name]; !ok {
			t.Errorf("fichier manquant : %s", name)
		}
	}
	if _, ok := f["gwdns/dormant-open.conf"]; ok {
		t.Error("un module non branché n'a pas d'instance DNS")
	}
	has(t, f, "gwdns/pentest-closed.conf", "port=5302")
	has(t, f, "gwdns/pentest-open.conf", "port=5303")
	has(t, f, "networkd/50-gw-pentest.network", "MACAddress=52:54:00:fe:42:42")
	has(t, f, "networkd/50-gw-pentest.network", "Address=10.66.66.254/24")
}

func TestDHCP(t *testing.T) {
	f := render(t, input())
	d := "gwdns/dhcp.conf"
	has(t, f, d, "dhcp-range=set:pentest,10.66.66.100,10.66.66.199,255.255.255.0,12h")
	has(t, f, d, "dhcp-option=tag:demo,option:domain-search,demo.client.omedo.lab")
	has(t, f, d, "dhcp-host=52:54:00:aa:bb:cc,10.77.77.40,dc01") // réservation de core.conf
	if !strings.Contains(f[d], "dhcp-host=52:54:00:66:66:10,10.66.66.10,kali\ndhcp-host=52:54:00:66:66:20,10.66.66.20,msf2\ndhcp-host=52:54:00:66:66:30,10.66.66.30,web\n") {
		t.Errorf("réservations triées par adresse attendues :\n%s", f[d])
	}
	if strings.Contains(f[d], "dnsonly") || strings.Contains(f[d], "dormant") {
		t.Error("pas de réservation sans MAC, ni pour un module non branché")
	}
}

func TestDNSVisibility(t *testing.T) {
	f := render(t, input())
	p := "gwdns/pentest-closed.conf"
	// Ses propres noms, alias triés, noms DHCP dynamiques
	has(t, f, p, "host-record=kali.pentest-lab.omedo.lab,10.66.66.10")
	has(t, f, p, "host-record=dnsonly.pentest-lab.omedo.lab,10.66.66.50")
	has(t, f, p, "cname=dvwa.pentest-lab.omedo.lab,web.pentest-lab.omedo.lab")
	has(t, f, p, "cname=ext.pentest-lab.omedo.lab,srv.demo.client.omedo.lab") // cible complète gardée telle quelle
	has(t, f, p, "hostsdir=/run/gwdns/leases/pentest")
	// Lien vers demo : visible, avec ses noms dynamiques ; lien vers dormant (non branché) : noms fixes seulement
	has(t, f, p, "host-record=srv.demo.client.omedo.lab,10.77.77.10")
	has(t, f, p, "hostsdir=/run/gwdns/leases/demo")
	has(t, f, p, "host-record=x.dormant.omedo.lab,10.88.88.10")
	hasNot(t, f, p, "hostsdir=/run/gwdns/leases/dormant")
	// Tous les domaines restent locaux (jamais relayés), y compris la racine
	for _, dom := range []string{"omedo.lab", "demo.client.omedo.lab", "dormant.omedo.lab", "pentest-lab.omedo.lab"} {
		has(t, f, p, "local=/"+dom+"/")
	}
	// Instance fermée : pas de relais ; instance ouverte : relais vers l'amont
	if strings.Contains(f[p], "server=") {
		t.Error("l'instance closed ne doit relayer vers aucun serveur")
	}
	has(t, f, "gwdns/pentest-open.conf", "server=192.168.122.1")

	// demo n'a pas de lien vers pentest : il ne le voit pas
	d := "gwdns/demo-open.conf"
	has(t, f, d, "# module pentest (pentest-lab.omedo.lab) : masqué")
	if strings.Contains(f[d], "kali.pentest") {
		t.Error("demo ne doit pas résoudre les noms de pentest (lien à sens unique)")
	}
}

func TestLinksAndBlocked(t *testing.T) {
	f := render(t, input())
	// pentest -> demo seulement : dormant n'est pas branché, aucune règle vers lui
	if f["gwdns/links"] != "10.66.66.0/24 10.77.77.0/24\n" {
		t.Errorf("links :\n%s", f["gwdns/links"])
	}
	if f["gwdns/blocked"] != "10.66.66.20\n" {
		t.Errorf("blocked :\n%s", f["gwdns/blocked"])
	}
}

func TestErrorsAndEmpty(t *testing.T) {
	in := input()
	in.Modules[2].NoInternet = []string{"fantome"}
	if _, err := Render(in); err == nil {
		t.Error("NO_INTERNET d'une VM inconnue doit être une erreur")
	}
	in = input()
	in.Upstream = ""
	if _, err := Render(in); err == nil {
		t.Error("sans DNS amont, des modules branchés doivent être une erreur")
	}
	in = input()
	in.Modules[0].Subnet = "pas.un.reseau"
	if _, err := Render(in); err == nil {
		t.Error("sous-réseau invalide doit être une erreur")
	}
	// Aucun module branché : fichiers vides mais présents (gwctl et gwdhcp-hook les lisent)
	in = input()
	in.Attached = nil
	f := render(t, in)
	if f["gwdns/modules"] != "" || f["gwdns/links"] != "" || !strings.Contains(f["gwdns/dhcp.conf"], "dhcp-authoritative") {
		t.Errorf("sans module branché : %v", f)
	}
}

func TestGatewayMAC(t *testing.T) {
	for subnet, want := range map[string]string{"10.66.66": "52:54:00:fe:42:42", "172.16.5": "52:54:00:fe:10:05", "10.255.0": "52:54:00:fe:ff:00"} {
		if got := GatewayMAC(subnet); got != want {
			t.Errorf("GatewayMAC(%s) = %s, attendu %s", subnet, got, want)
		}
	}
}

func TestConnections(t *testing.T) {
	in := input()
	in.Modules[0].Conns = []module.Connection{ // demo
		{To: "pentest/web", From: []string{"srv"}, Ports: []string{"tcp/80", "tcp/8000-8100", "udp/53", "icmp"}},
		{To: "internet", From: []string{"dc01"}, Ports: []string{"tcp/443"}},
		{To: "dormant"}, // non branché : ignorée
	}
	f := render(t, in)
	fm, fi := "gwdns/forward-modules.nft", "gwdns/forward-internet.nft"
	// Lien existant (pentest -> demo) gardé, connexion restreinte : un accept par protocole
	has(t, f, fm, `oifname != "eth0" ip saddr 10.66.66.0/24 ip daddr 10.77.77.0/24 accept`)
	has(t, f, fm, `oifname != "eth0" ip saddr { 10.77.77.10 } ip daddr 10.66.66.30 tcp dport { 80, 8000-8100 } accept`)
	has(t, f, fm, `oifname != "eth0" ip saddr { 10.77.77.10 } ip daddr 10.66.66.30 udp dport { 53 } accept`)
	has(t, f, fm, `oifname != "eth0" ip saddr { 10.77.77.10 } ip daddr 10.66.66.30 meta l4proto icmp accept`)
	has(t, f, fm, "# connexion demo -> dormant (tous ports) : module non branché, ignorée")
	// Internet : seulement les ports déclarés quand c'est ouvert, puis refus pour cette source
	has(t, f, fi, "ip saddr { 10.77.77.40 } ip saddr @allowed ip daddr != @private tcp dport { 443 } accept")
	has(t, f, fi, "ip saddr { 10.77.77.40 } reject with icmpx admin-prohibited")
	// La cible devient résolvable (pentest visible depuis demo)
	has(t, f, "gwdns/demo-open.conf", "# module pentest (pentest-lab.omedo.lab) : visible")

	// Source ou cible sans IP fixe : erreur
	in.Modules[0].Conns = []module.Connection{{To: "pentest", From: []string{"inconnue"}}}
	if _, err := Render(in); err == nil {
		t.Error("source sans IP fixe acceptée")
	}
	in.Modules[0].Conns = []module.Connection{{To: "pentest/absente"}}
	if _, err := Render(in); err == nil {
		t.Error("VM cible inconnue acceptée")
	}
}

func TestPortal(t *testing.T) {
	in := input()
	no := false
	in.Modules[0].Conns = []module.Connection{ // demo
		{To: "internet", From: []string{"srv"}, Via: "portal", Portal: &module.Portal{Auth: "password", Password: "lab", Duration: "30m", API: true, HTTPS: "drop"}},
		{To: "pentest/web", Ports: []string{"tcp/80"}, Via: "portal", Portal: &module.Portal{Detect: &no}},
	}
	f := render(t, in)
	// Un ensemble par portail, entrées à expiration
	has(t, f, "gwdns/sets.nft", "set portal_0 { type ipv4_addr; flags timeout; }")
	has(t, f, "gwdns/sets.nft", "set portal_1 { type ipv4_addr; flags timeout; }")
	// Avant authentification : HTTP redirigé vers le portail ; DNS relayé (Internet) sauf VMs bloquées
	pr := "gwdns/prerouting.nft"
	has(t, f, pr, "ip saddr { 10.77.77.10 } ip saddr != @portal_0 ip daddr != @private tcp dport 80 redirect to :8800")
	has(t, f, pr, "ip saddr { 10.77.77.10 } ip saddr != @blocked meta l4proto { tcp, udp } th dport 53 redirect to :5301")
	has(t, f, pr, "ip saddr 10.77.77.0/24 ip saddr != @portal_1 ip daddr 10.66.66.30 tcp dport 80 redirect to :8801")
	// Après : la connexion, pour les sources authentifiées seulement ; HTTPS non authentifié ignoré (drop)
	fi := "gwdns/forward-internet.nft"
	has(t, f, fi, "ip saddr { 10.77.77.10 } ip saddr @portal_0 ip daddr != @private accept")
	has(t, f, fi, "ip saddr { 10.77.77.10 } ip daddr != @private tcp dport 443 drop")
	has(t, f, fi, "ip saddr { 10.77.77.10 } reject with icmpx admin-prohibited")
	has(t, f, "gwdns/forward-modules.nft", `oifname != "eth0" ip saddr 10.77.77.0/24 ip saddr @portal_1 ip daddr 10.66.66.30 tcp dport { 80 } accept`)
	// Annonce RFC 8910 sur le module des sources
	has(t, f, "gwdns/dhcp.conf", `dhcp-option-force=tag:demo,114,"http://10.77.77.254:8800/api"`)
	// Configuration lue par le service de portail
	var pc []PortalConf
	if err := json.Unmarshal([]byte(f["gwdns/portal.json"]), &pc); err != nil || len(pc) != 2 {
		t.Fatalf("portal.json : %v\n%s", err, f["gwdns/portal.json"])
	}
	if p := pc[0]; p.Port != 8800 || p.Auth != "password" || p.Seconds != 1800 || !p.Detect || p.Gateway != "10.77.77.254" || p.Sources[0] != "10.77.77.10" {
		t.Errorf("portail 0 : %+v", p)
	}
	if p := pc[1]; p.Auth != "click" || p.Seconds != 3600 || p.Detect || p.Sources[0] != "10.77.77.0/24" {
		t.Errorf("portail 1 : %+v", p)
	}
}

func TestProxy(t *testing.T) {
	in := input()
	in.Modules[0].Conns = []module.Connection{ // demo
		{To: "internet", From: []string{"srv"}, Via: "proxy", Proxy: &module.Proxy{Mode: "filter", Allow: []string{"*.debian.org"}, Ports: []string{"tcp/8080"}}},
		{To: "pentest/web", Ports: []string{"tcp/80", "tcp/22"}, Via: "proxy", Proxy: &module.Proxy{Mode: "intercept"}},
	}
	f := render(t, in)
	pr := "gwdns/prerouting.nft"
	// Internet : seulement quand c'est ouvert ; jamais pour une VM bloquée
	has(t, f, pr, "ip saddr { 10.77.77.10 } ip saddr != @blocked ip saddr @allowed ip daddr != @private tcp dport { 80, 443, 8080 } redirect to :8080")
	has(t, f, pr, "ip saddr 10.77.77.0/24 ip saddr != @blocked ip daddr 10.66.66.30 tcp dport { 80, 443 } redirect to :8080")
	// Les autres ports suivent la connexion (ici ssh direct)
	has(t, f, "gwdns/forward-modules.nft", `oifname != "eth0" ip saddr 10.77.77.0/24 ip daddr 10.66.66.30 tcp dport { 80, 22 } accept`)
	var pc []ProxyConf
	if err := json.Unmarshal([]byte(f["gwdns/proxy.json"]), &pc); err != nil || len(pc) != 2 {
		t.Fatalf("proxy.json : %v\n%s", err, f["gwdns/proxy.json"])
	}
	if p := pc[0]; p.Mode != "filter" || p.Dest != "internet" || p.Allow[0] != "*.debian.org" || p.Sources[0] != "10.77.77.10" {
		t.Errorf("proxy 0 : %+v", p)
	}
	if p := pc[1]; p.ID != 1 || p.Mode != "intercept" || p.Dest != "10.66.66.30" || p.Target != "pentest/web" {
		t.Errorf("proxy 1 : %+v", p)
	}
}
