package module

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// copie du vrai module pentest dans un dossier temporaire nommé « pentest »
func pentestCopy(t *testing.T) *Module {
	t.Helper()
	src := filepath.Join("..", "..", "..", "modules", "pentest")
	dir := filepath.Join(t.TempDir(), "pentest")
	os.MkdirAll(filepath.Join(dir, "files"), 0o755)
	for _, f := range []string{FileName, "files/compose.yaml", "files/nginx.conf"} {
		b, err := os.ReadFile(filepath.Join(src, f))
		if err != nil {
			t.Fatal(err)
		}
		os.WriteFile(filepath.Join(dir, f), b, 0o644)
	}
	m, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func reload(t *testing.T, m *Module) *Module {
	t.Helper()
	if err := m.Save(); err != nil {
		t.Fatal(err)
	}
	n, err := Load(m.Dir)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// changed renvoie les lignes de b absentes de a (à l'identique)
func changed(a, b string) []string {
	seen := map[string]int{}
	for _, l := range strings.Split(a, "\n") {
		seen[l]++
	}
	var out []string
	for _, l := range strings.Split(b, "\n") {
		if seen[l] > 0 {
			seen[l]--
			continue
		}
		out = append(out, l)
	}
	return out
}

func TestEditKeepsFormatting(t *testing.T) {
	m := pentestCopy(t)
	orig := string(m.orig)

	m.SetAlias("intranet", "web")
	m.AddHost(Host{Name: "dc01", IP: 40, MAC: "52:54:00:aa:bb:cc"})
	m.SetLink("demo", true)
	m = reload(t, m)
	added := changed(orig, string(m.orig))
	want := []string{"    intranet: web", "  hosts:", "    - {name: dc01, ip: 40, mac: '52:54:00:aa:bb:cc'}", "  links: [demo]"}
	if strings.Join(added, "|") != strings.Join(want, "|") {
		t.Errorf("seules les lignes éditées doivent changer :\n%q\nattendu\n%q", added, want)
	}
	if m.DNS.Aliases["intranet"] != "web" || len(m.DNS.Hosts) != 1 || m.DNS.Hosts[0].IP != 40 || m.DNS.Links[0] != "demo" {
		t.Errorf("éditions mal relues : %+v", m.DNS)
	}

	m.DelAlias("intranet")
	m.DelHost("dc01")
	m.SetLink("demo", false)
	m = reload(t, m)
	if string(m.orig) != orig {
		t.Errorf("après aller-retour, le fichier doit être identique à l'original :\n%s", string(m.orig))
	}
}

func TestEditVMAndNetwork(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "demo")
	os.MkdirAll(dir, 0o755)
	src := "# module minimal\nmodule: demo\n\nvms:\n  srv: {}   # tout par défaut\n  db: {ip: 20}\n  app:\n    ip: 21\n"
	os.WriteFile(filepath.Join(dir, FileName), []byte(src), 0o644)
	m, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if m.VMs["srv"] == nil || m.VMs["db"].IP != 20 || strings.Join(m.VMNames(), ",") != "srv,db,app" {
		t.Fatalf("lecture : %+v %v", m.VMs, m.VMNames())
	}
	m.SetNetwork("demo-lab", "10.70.70.0/24")
	if err := m.SetVMValue("db", "mac", "52:54:00:3a:91:c4"); err != nil {
		t.Fatal(err)
	}
	if err := m.SetVMValue("app", "mac", "52:54:00:3a:91:c5"); err != nil {
		t.Fatal(err)
	}
	m = reload(t, m)
	out := string(m.orig)
	for _, want := range []string{"# module minimal", "  srv: {}   # tout par défaut", "  db: {ip: 20, mac: '52:54:00:3a:91:c4'}", "    mac: 52:54:00:3a:91:c5", "network:\n  name: demo-lab\n  subnet: 10.70.70.0/24\nvms:"} {
		if !strings.Contains(out, want) {
			t.Errorf("%q absent :\n%s", want, out)
		}
	}
	if m.Subnet().String() != "10.70.70.0/24" || m.VMs["db"].MAC != "52:54:00:3a:91:c4" {
		t.Errorf("relecture : %v %q", m.Subnet(), m.VMs["db"].MAC)
	}
}
