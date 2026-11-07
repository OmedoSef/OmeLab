package coreconf

import (
	"strings"
	"testing"

	"labs/labctl/internal/module"
)

func safeModule() Module {
	return Module{
		Name: "pentest", Subnet: "10.66.66", Domain: "pentest.omelab.internal",
		Hosts:   map[string]int{"web": 30, "msf2": 20},
		MACs:    map[string]string{"web": "52:54:00:66:66:30"},
		Aliases: map[string]string{"dvwa": "web", "ext": "srv.autre.omelab.internal"},
		Conns:   []module.Connection{{To: "internet", From: []string{"web"}, Ports: []string{"tcp/443"}}},
	}
}

// Injections rejouées depuis la revue de sécurité : chaque identifiant recopié dans nftables, dnsmasq ou
// networkd doit être refusé s'il porte de quoi casser la ligne ou sortir du fichier.
func TestCheckRejectsInjection(t *testing.T) {
	nft := "a\n    iifname \"eth0\" accept #"
	cases := map[string]func(m *Module){
		"vm (règle nft)":            func(m *Module) { m.Hosts[nft] = 40 },
		"vm (directive dnsmasq)":    func(m *Module) { m.Hosts["web\nserver=1.1.1.1"] = 41 },
		"source de connexion":       func(m *Module) { m.Conns[0].From = []string{nft} },
		"module (fichier ailleurs)": func(m *Module) { m.Name = "../etc/x" },
		"module (accolade)":         func(m *Module) { m.Name = "a}" },
		"domaine":                   func(m *Module) { m.Domain = "x.lab\nserver=1.1.1.1" },
		"sous-réseau":               func(m *Module) { m.Subnet = "10.66.66 accept" },
		"MAC":                       func(m *Module) { m.MACs["web"] = "52:54:00:66:66:30\ndhcp-script=/tmp/x" },
		"alias":                     func(m *Module) { m.Aliases["a;b"] = "web" },
		"cible d'alias":             func(m *Module) { m.Aliases["ok"] = "web\naddress=/#/1.2.3.4" },
		"cible de connexion":        func(m *Module) { m.Conns[0].To = "pentest/web}" },
		"lien":                      func(m *Module) { m.Links = []string{"x\ny"} },
		"VM branchée":               func(m *Module) { m.NoRoute = map[string]bool{"k\"ali": true} },
		"octet":                     func(m *Module) { m.Hosts["web"] = 300 },
	}
	for name, mutate := range cases {
		m := safeModule()
		mutate(&m)
		if err := m.Check(); err == nil {
			t.Errorf("%s : identifiant dangereux accepté", name)
		}
		// Render refuse aussi, quel que soit l'appelant (défense en profondeur)
		if _, err := Render(Input{Modules: []Module{m}, RootDomain: "omelab.internal"}); err == nil {
			t.Errorf("%s : Render a généré la configuration malgré tout", name)
		}
	}
}

func TestCheckAcceptsNormalModule(t *testing.T) {
	m := safeModule()
	if err := m.Check(); err != nil {
		t.Fatal(err)
	}
	in := Input{Modules: []Module{m}, Attached: []string{"pentest"}, Upstream: "192.168.122.1", RootDomain: "omelab.internal"}
	if err := in.Check(); err != nil {
		t.Fatal(err)
	}
	if _, err := Render(in); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []Input{
		{Modules: []Module{m}, RootDomain: "x\nserver=1.1.1.1"},
		{Modules: []Module{m}, RootDomain: "omelab.internal", Upstream: "1.1.1.1\nserver=8.8.8.8"},
	} {
		if err := bad.Check(); err == nil || !strings.Contains(err.Error(), "invalide") {
			t.Errorf("entrée dangereuse acceptée : %+v", bad.RootDomain+bad.Upstream)
		}
	}
}
