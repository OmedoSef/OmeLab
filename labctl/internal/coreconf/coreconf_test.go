package coreconf

import (
	"strings"
	"testing"
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
