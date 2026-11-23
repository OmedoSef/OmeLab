package lab

import (
	"bytes"
	"slices"
	"strings"
	"testing"

	"labs/labctl/internal/virt"
	"labs/labctl/internal/virt/virtfake"
)

// wholeLab : « zatk » fournit une VM branchée sur « cible » (attach:), et « seul » est indépendant. L'ordre des
// noms (cible, seul, zatk) n'est donc pas l'ordre de construction : zatk doit passer avant cible.
func wholeLab(t *testing.T) (*Lab, *virtfake.Fake) {
	t.Helper()
	return engineLab(t, map[string]string{
		"zatk":  "module: zatk\nnetwork: {subnet: 10.95.95.0/24}\nvms:\n  kali: {ip: 10}\n",
		"cible": "module: cible\nnetwork: {subnet: 10.96.96.0/24}\nattach:\n  - {vm: zatk/kali, ip: 50, mac: \"52:54:00:96:96:50\"}\nvms:\n  srv: {ip: 10}\n",
		"seul":  "module: seul\nnetwork: {subnet: 10.97.97.0/24}\nvms:\n  a: {ip: 10}\n",
	})
}

func TestModuleOrder(t *testing.T) {
	l, _ := wholeLab(t)
	if got, err := l.ModuleOrder(nil); err != nil || !slices.Equal(got, []string{"zatk", "cible", "seul"}) {
		t.Errorf("ordre : %v %v", got, err)
	}
	if got, _ := l.ModuleOrder([]string{"seul", "cible"}); !slices.Equal(got, []string{"cible", "seul"}) {
		t.Errorf("sous-ensemble : %v", got)
	}
	if _, err := l.ModuleOrder([]string{"absent"}); err == nil || !strings.Contains(err.Error(), "module inconnu") {
		t.Errorf("module inconnu : %v", err)
	}
}

// Les VMs existent (construites) : start démarre la passerelle puis tous les modules dans l'ordre, stop les arrête
// dans l'ordre inverse puis arrête la passerelle ; stop d'un seul module laisse la passerelle aux autres.
func TestStartStopAll(t *testing.T) {
	l, f := wholeLab(t)
	for _, m := range []struct{ mod, vm, prefix string }{{"zatk", "kali", "10.95.95"}, {"cible", "srv", "10.96.96"}, {"seul", "a", "10.97.97"}} {
		isolatedNet(f, netName(t, l, m.mod), m.prefix, false)
		vmOf(t, l, f, m.mod, m.vm, "shut off")
	}
	// la carte de zatk-kali sur le réseau de cible (branchement)
	k := f.VM("zatk-kali")
	k.Ifaces = append(k.Ifaces, virt.Interface{Type: "network", Network: netName(t, l, "cible"), MAC: "52:54:00:96:96:50"})
	f.AddVM(*k)
	f.AddVM(virtfake.VM{Name: GatewayVM, State: "shut off"})
	f.Calls = nil
	var out bytes.Buffer
	if err := l.StartAll(nil, &out); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	var starts []string
	for _, c := range f.Calls {
		if strings.HasPrefix(c, "start ") {
			starts = append(starts, strings.TrimPrefix(c, "start "))
		}
	}
	if !slices.Equal(starts, []string{GatewayVM, "zatk-kali", "cible-srv", "seul-a"}) {
		t.Errorf("ordre de démarrage : %v", starts)
	}

	f.Calls = nil
	if err := l.StopAll([]string{"seul"}, false, &out); err != nil {
		t.Fatal(err)
	}
	if f.VM("seul-a").State != "shut off" || f.VM(GatewayVM).State != virt.StateRunning {
		t.Errorf("stop seul : seul-a %s, passerelle %s", f.VM("seul-a").State, f.VM(GatewayVM).State)
	}
	f.Calls = nil
	if err := l.StopAll(nil, false, &out); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(f.Calls, []string{"shutdown cible-srv", "shutdown zatk-kali", "shutdown " + GatewayVM}) {
		t.Errorf("ordre d'arrêt : %v", f.Calls)
	}
}

// Un module jamais construit est signalé par start (labctl up), sans erreur ; up construit tout, passerelle comprise
// si elle existe déjà (démarrée), modules dans l'ordre.
func TestStartUpNotBuilt(t *testing.T) {
	l, f := wholeLab(t)
	var out bytes.Buffer
	if err := l.StartAll(nil, &out); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "pas de passerelle") || !strings.Contains(out.String(), "cible n'est pas construit") {
		t.Errorf("signalements :\n%s", out.String())
	}
	for _, m := range []struct{ mod, vm string }{{"zatk", "kali"}, {"cible", "srv"}, {"seul", "a"}} {
		vmOf(t, l, f, m.mod, m.vm, "shut off") // VMs déjà là : up les reprend sans rien créer
	}
	// passerelle existante, arrêtée : up la démarre puis lui applique la configuration (fausse passerelle joignable)
	f.AddNetwork(virt.Network{Name: "default", Forward: "nat", HostIP: "192.168.122.1", Prefix: 24, Active: true})
	f.AddVM(virtfake.VM{Name: GatewayVM, State: "shut off", IP: "192.168.122.46"})
	gw := &fakeGW{addrs: []string{"10.95.95.254/24", "10.96.96.254/24", "10.97.97.254/24"}}
	l.Shell = gw.shell
	out.Reset()
	if err := l.UpAll(nil, UpOptions{Out: &out}); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	o := out.String()
	if f.VM(GatewayVM).State != virt.StateRunning || strings.Index(o, "Module zatk") > strings.Index(o, "Module cible") {
		t.Errorf("up : passerelle %s\n%s", f.VM(GatewayVM).State, o)
	}
	for _, m := range []string{"zatk", "cible", "seul"} {
		if f.Network(netName(t, l, m)) == nil {
			t.Errorf("réseau de %s non créé", m)
		}
	}
}
