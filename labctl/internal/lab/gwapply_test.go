package lab

import (
	"archive/tar"
	"bytes"
	"errors"
	"io"
	"regexp"
	"slices"
	"strings"
	"testing"

	"labs/labctl/internal/coreconf"
	"labs/labctl/internal/virt"
	"labs/labctl/internal/virt/virtfake"
)

// fakeGW joue la passerelle derrière Lab.Shell : adresses .254 des réseaux branchés, état de gwctl (allow, revoke,
// status), archive de configuration reçue. Elle note chaque script.
type fakeGW struct {
	addrs   []string // « 10.81.81.254/24 »…
	allowed []string
	files   []string // contenu de l'archive de configuration
	scripts []string
}

var reGwctl = regexp.MustCompile(`^sudo gwctl (allow|revoke|status) ?(\S*)$`)

func (f *fakeGW) shell(_, script string, stdin io.Reader) ([]byte, []byte, error) {
	f.scripts = append(f.scripts, script)
	if m := reGwctl.FindStringSubmatch(script); m != nil {
		switch m[1] {
		case "allow":
			f.allowed = append(f.allowed, m[2])
		case "revoke":
			f.allowed = slices.DeleteFunc(f.allowed, func(x string) bool { return x == m[2] })
		case "status":
			var b strings.Builder
			for _, x := range f.allowed {
				b.WriteString("allowed " + x + "\n")
			}
			return []byte(b.String()), nil, nil
		}
		return nil, nil, nil
	}
	switch {
	case strings.Contains(script, "ip -4 -br addr"):
		return []byte("lo UNKNOWN 127.0.0.1/8\nenp1s0 UP 192.168.122.46/24\nenp2s0 UP " + strings.Join(f.addrs, " ") + "\n"), nil, nil
	case strings.Contains(script, "tar -C $t"):
		tr := tar.NewReader(stdin)
		for {
			h, err := tr.Next()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				return nil, []byte(err.Error()), err
			}
			if h.Typeflag == tar.TypeReg {
				f.files = append(f.files, h.Name)
			}
		}
	}
	return nil, nil, nil
}

// gatewayLab : lab vulnerableLab (modules « good » 10.81.81 et « vuln » 10.80.80, vulnérable) avec une passerelle
// en marche branchée sur les deux réseaux.
func gatewayLab(t *testing.T) (*Lab, *fakeGW) {
	t.Helper()
	l := vulnerableLab(t)
	f := l.Virt.(*virtfake.Fake)
	var ifs []virt.Interface
	for _, p := range []string{"10.81.81", "10.80.80"} {
		ifs = append(ifs, virt.Interface{Type: "network", MAC: coreconf.GatewayMAC(p), Network: "x"})
	}
	f.AddNetwork(virt.Network{Name: "default", Forward: "nat", HostIP: "192.168.122.1", Prefix: 24, Active: true}) // DNS amont
	f.AddVM(virtfake.VM{Name: GatewayVM, State: virt.StateRunning, IP: "192.168.122.46", MemMiB: GatewayProxyMiB, Ifaces: ifs})
	gw := &fakeGW{addrs: []string{"10.81.81.254/24", "10.80.80.254/24"}}
	l.Shell = gw.shell
	return l, gw
}

// core apply : la configuration générée est envoyée, une instance DNS « open » et « closed » par module branché est
// relancée, et une ouverture d'Internet restée sur le module vulnérable est refermée (pas celle de l'autre module).
func TestGatewayApply(t *testing.T) {
	l, gw := gatewayLab(t)
	gw.allowed = []string{"10.80.80.30", "10.81.81.0/24"}
	var out bytes.Buffer
	if err := l.Gateway(&out).Apply(); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	for _, want := range []string{"gwdns/dhcp.conf", "gwdns/good-open.conf", "gwdns/good-closed.conf", "gwdns/vuln-closed.conf"} {
		if !slices.Contains(gw.files, want) {
			t.Errorf("%s absent de la configuration envoyée : %v", want, gw.files)
		}
	}
	restart := ""
	for _, s := range gw.scripts {
		if strings.Contains(s, "sudo gwctl apply") {
			restart = s
		}
	}
	for _, inst := range []string{"dhcp", "good-closed", "good-open", "vuln-closed", "vuln-open"} {
		if !strings.Contains(restart, inst) {
			t.Errorf("instance DNS %s non relancée :\n%s", inst, restart)
		}
	}
	if !slices.Equal(gw.allowed, []string{"10.81.81.0/24"}) {
		t.Errorf("autorisations après apply : %v", gw.allowed)
	}
	if !strings.Contains(out.String(), "Internet refermé pour 10.80.80.30") {
		t.Errorf("fermeture non signalée :\n%s", out.String())
	}
}

// core internet on : ouvre le réseau du module, ou une VM par son adresse ; refusé pour le module vulnérable avant
// tout accès à la passerelle.
func TestGatewayInternetOn(t *testing.T) {
	l, gw := gatewayLab(t)
	g := l.Gateway(io.Discard)
	if err := g.InternetOn("good", nil); err != nil {
		t.Fatal(err)
	}
	if err := g.InternetOn("good", []string{"srv"}); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(gw.allowed, []string{"10.81.81.0/24", "10.81.81.10"}) {
		t.Errorf("autorisations : %v", gw.allowed)
	}
	n := len(gw.scripts)
	if err := g.InternetOn("vuln", nil); err == nil {
		t.Fatal("module vulnérable ouvert")
	}
	if len(gw.scripts) != n {
		t.Errorf("la passerelle a été contactée pour un module vulnérable : %q", gw.scripts[n:])
	}
	// Passerelle non branchée sur le réseau du module : refus
	gw.addrs = nil
	if err := g.InternetOn("good", nil); err == nil || !strings.Contains(err.Error(), "non branchée") {
		t.Errorf("passerelle non branchée : %v", err)
	}
}

// La suppression d'une VM libère ses baux DHCP sur la passerelle (gwctl release avec ses MAC).
func TestReleaseLeases(t *testing.T) {
	l, gw := gatewayLab(t)
	var out bytes.Buffer
	l.releaseLeases([]string{"52:54:00:aa:bb:01", "52:54:00:aa:bb:02"}, &out)
	if !slices.Contains(gw.scripts, "sudo gwctl release 52:54:00:aa:bb:01 52:54:00:aa:bb:02") {
		t.Errorf("scripts envoyés : %q\n%s", gw.scripts, out.String())
	}
}

// Clavier d'une VM par SSH : sonde, puis le script passé sur l'entrée standard de « sudo sh -s ».
func TestVMKeyboardOverSSH(t *testing.T) {
	l, _ := gatewayLab(t)
	f := l.Virt.(*virtfake.Fake)
	f.AddVM(virtfake.VM{Name: "good-srv", State: virt.StateRunning, IP: "10.81.81.10",
		Ifaces: []virt.Interface{{Type: "network", Network: "x", MAC: "52:54:00:81:81:10"}}})
	var got []string
	var stdin string
	l.Shell = func(target, script string, in io.Reader) ([]byte, []byte, error) {
		got = append(got, target+" "+script)
		if in != nil {
			b, _ := io.ReadAll(in)
			stdin = string(b)
		}
		return []byte("ok\n"), nil, nil
	}
	if err := l.VMKeyboard("good-srv", "fr", io.Discard); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, []string{"lab@10.81.81.10 true", "lab@10.81.81.10 sudo sh -s"}) || !strings.Contains(stdin, "fr") {
		t.Errorf("SSH : %q, script %q", got, stdin)
	}
}
