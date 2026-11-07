package module

import "testing"

func TestModuleExpandVars(t *testing.T) {
	m := &Module{
		Module:  "demo",
		Vars:    map[string]string{"iso": "~/img/${var.rel}.iso", "rel": "win2022", "net": "${network.subnet}"},
		Network: &Network{Subnet: "10.5.5.0/24"},
		DNS:     DNS{Aliases: map[string]string{"www": "${vms.srv.fqdn}"}},
		Connections: []Connection{
			{To: "internet", From: []string{"srv"}, Ports: []string{"tcp/443"}},
		},
		VMs: map[string]*VM{
			"srv": {
				IP:       10,
				Windows:  &Windows{ISO: "${var.iso}", Password: "${var.rel}-pw"},
				Run:      []string{"echo ${vm.fqdn} ${vm.ip} gw ${network.gateway} mod ${module.name}"},
				Packages: []string{"pkg-${var.rel}"},
			},
		},
		VMOrder: []string{"srv"},
	}
	if err := m.ExpandVars("omedo.lab", "fr", nil); err != nil {
		t.Fatalf("ExpandVars : %v", err)
	}
	// vars: résolu (var -> var, var -> global)
	if got := m.Vars["iso"]; got != "~/img/win2022.iso" {
		t.Errorf("vars.iso = %q", got)
	}
	if got := m.Vars["net"]; got != "10.5.5.0/24" {
		t.Errorf("vars.net = %q", got)
	}
	// champ de VM : ${var.*} et ${vm.*}
	w := m.VMs["srv"].Windows
	if w.ISO != "~/img/win2022.iso" {
		t.Errorf("iso = %q", w.ISO)
	}
	if w.Password != "win2022-pw" {
		t.Errorf("password = %q", w.Password)
	}
	if got := m.VMs["srv"].Run[0]; got != "echo srv.demo-lab.omedo.lab 10.5.5.10 gw 10.5.5.254 mod demo" {
		t.Errorf("run = %q", got)
	}
	if got := m.VMs["srv"].Packages[0]; got != "pkg-win2022" {
		t.Errorf("packages = %q", got)
	}
	// niveau module : ${vms.<nom>.*} dans un alias DNS
	if got := m.DNS.Aliases["www"]; got != "srv.demo-lab.omedo.lab" {
		t.Errorf("alias www = %q", got)
	}
}

func TestModuleExpandVarsLabOverride(t *testing.T) {
	m := &Module{
		Module:  "demo",
		Vars:    map[string]string{"iso": "/defaut.iso", "pw": "defautpw"},
		Network: &Network{Subnet: "10.5.5.0/24"},
		VMs: map[string]*VM{
			"srv": {IP: 10, Windows: &Windows{ISO: "${var.iso}", Password: "${var.pw}"},
				Run: []string{"echo ${var.images}"}}, // présent seulement au niveau lab
		},
		VMOrder: []string{"srv"},
	}
	lab := map[string]string{"iso": "/monlab.iso", "images": "/srv/img"} // surcharge iso, ajoute images
	if err := m.ExpandVars("omedo.lab", "fr", lab); err != nil {
		t.Fatalf("ExpandVars : %v", err)
	}
	w := m.VMs["srv"].Windows
	if w.ISO != "/monlab.iso" { // le lab surcharge le défaut du module
		t.Errorf("iso = %q, veut /monlab.iso", w.ISO)
	}
	if w.Password != "defautpw" { // pas surchargé : défaut du module conservé
		t.Errorf("password = %q, veut defautpw", w.Password)
	}
	if got := m.VMs["srv"].Run[0]; got != "echo /srv/img" { // var seulement dans le lab
		t.Errorf("run = %q, veut 'echo /srv/img'", got)
	}
}

func TestResolveLabVars(t *testing.T) {
	// var -> var dans le bloc du lab, et détection de cycle
	ok := map[string]string{"base": "/srv", "images": "${var.base}/img", "vms": "${var.base}/vms"}
	if err := ResolveLabVars(ok, "fr"); err != nil {
		t.Fatalf("ResolveLabVars : %v", err)
	}
	if ok["images"] != "/srv/img" || ok["vms"] != "/srv/vms" {
		t.Errorf("résolu = %v", ok)
	}
	cyc := map[string]string{"a": "${var.b}", "b": "${var.a}"}
	if err := ResolveLabVars(cyc, "fr"); err == nil {
		t.Error("cycle de lab vars accepté")
	}
}

func TestModuleExpandVarsErrors(t *testing.T) {
	// ${vm.*} au niveau module : erreur (pas de VM courante)
	m := &Module{
		Module:      "demo",
		Network:     &Network{Subnet: "10.5.5.0/24"},
		Connections: []Connection{{To: "internet", From: []string{"${vm.name}"}}},
		VMs:         map[string]*VM{"srv": {IP: 10}},
		VMOrder:     []string{"srv"},
	}
	if err := m.ExpandVars("omedo.lab", "fr", nil); err == nil {
		t.Error("${vm.*} au niveau module accepté")
	}
	// variable inconnue
	m2 := &Module{Module: "demo", Network: &Network{Subnet: "10.5.5.0/24"},
		VMs: map[string]*VM{"srv": {IP: 10, Run: []string{"echo ${var.absent}"}}}, VMOrder: []string{"srv"}}
	if err := m2.ExpandVars("omedo.lab", "fr", nil); err == nil {
		t.Error("${var.absent} inconnu accepté")
	}
	// cycle entre variables
	m3 := &Module{Module: "demo", Vars: map[string]string{"a": "${var.b}", "b": "${var.a}"},
		Network: &Network{Subnet: "10.5.5.0/24"}, VMs: map[string]*VM{"srv": {IP: 10}}, VMOrder: []string{"srv"}}
	if err := m3.ExpandVars("omedo.lab", "fr", nil); err == nil {
		t.Error("cycle de vars accepté")
	}
}
