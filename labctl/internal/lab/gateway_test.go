package lab

import (
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"labs/labctl/internal/testtool"
)

func TestGatewayFiles(t *testing.T) {
	l := testLab(t)
	for _, f := range gwInstall {
		b, err := l.GatewayFile(f.name)
		if err != nil || len(b) == 0 {
			t.Errorf("%s non embarqué : %v", f.name, err)
		}
	}
	// Un fichier local remplace celui de l'exécutable
	os.MkdirAll(filepath.Join(l.Root, "core", "files"), 0o755)
	os.WriteFile(filepath.Join(l.Root, "core", "files", "gwctl"), []byte("#!/bin/sh\n# local\n"), 0o755)
	if b, _ := l.GatewayFile("gwctl"); !strings.Contains(string(b), "# local") {
		t.Error("remplacement local ignoré")
	}
}

func TestInternetStateText(t *testing.T) {
	cases := []struct {
		s    InternetState
		want string
	}{
		{InternetState{Module: "m"}, "non branché (labctl core attach m)"},
		{InternetState{Module: "m", Attached: true}, "fermé"},
		{InternetState{Module: "m", Attached: true, All: true, Never: []string{"x"}}, "OUVERT : tout le module (jamais : x)"},
		{InternetState{Module: "m", Attached: true, VMs: []string{"a", "b"}}, "OUVERT : a b"},
	}
	for _, c := range cases {
		if got := c.s.String(); got != c.want {
			t.Errorf("%+v : %q, attendu %q", c.s, got, c.want)
		}
	}
}

// Les autorisations Internet laissées sur un module vulnérable (installation interrompue) sont repérées, adresse
// seule comme réseau entier ; celles des autres modules restent.
func TestVulnerableGrants(t *testing.T) {
	status := "allowed 10.66.66.30\nallowed 10.73.73.0/24\nallowed 10.66.66.0/24\nblocked 10.66.66.31\nallowed pas-une-ip\n"
	red := []netip.Prefix{netip.MustParsePrefix("10.66.66.0/24")}
	if got := vulnerableGrants(status, red); !slices.Equal(got, []string{"10.66.66.30", "10.66.66.0/24"}) {
		t.Errorf("autorisations à refermer : %q", got)
	}
}

// Seule une image ISO 9660 est attachée comme ISO Windows, jamais un autre fichier de l'hôte.
func TestIsISO9660(t *testing.T) {
	dir := t.TempDir()
	iso := make([]byte, 0x8010)
	copy(iso[0x8001:], "CD001")
	files := map[string][]byte{"ok.iso": iso, "cle": []byte("-----BEGIN OPENSSH PRIVATE KEY-----"), "court.iso": iso[:0x8003]}
	for name, b := range files {
		if err := os.WriteFile(filepath.Join(dir, name), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for name, ok := range map[string]bool{"ok.iso": true, "cle": false, "court.iso": false} {
		p := filepath.Join(dir, name)
		fi, _ := os.Stat(p)
		if err := isISO9660(p, fi); (err == nil) != ok {
			t.Errorf("%s : %v", name, err)
		}
	}
	fi, _ := os.Stat(dir)
	if isISO9660(dir, fi) == nil {
		t.Error("dossier accepté comme ISO")
	}
}

// Un module dont le domaine est un domaine d'Internet (module.yaml modifié sans validation) est écarté de la
// configuration de la passerelle.
func TestCoreInputDropsPublicDomain(t *testing.T) {
	l := vulnerableLab(t)
	dir := filepath.Join(l.Root, "modules", "evil")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "module.yaml"), []byte("module: evil\nnetwork: {subnet: 10.82.82.0/24, domain: github.com}\nvms:\n  www: {ip: 10}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	in, err := l.CoreInput()
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range in.Modules {
		if m.Name == "evil" {
			t.Fatal("module au domaine d'Internet transmis à la passerelle")
		}
	}
	if !slices.ContainsFunc(in.Skipped, func(s string) bool { return strings.Contains(s, "evil") && strings.Contains(s, "github.com") }) {
		t.Errorf("écart non signalé : %q", in.Skipped)
	}
}

// Le module mitmproxy de la passerelle (Python) : choix de la règle, filtre par domaine (testdata/gwproxy_test.py).
func TestGwproxyPython(t *testing.T) {
	py := testtool.Need(t, "python3")
	cmd := exec.Command(py, "-I", "-B", filepath.Join("testdata", "gwproxy_test.py"))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
}
