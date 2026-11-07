package lab

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"labs/labctl/internal/coreconf"
	"labs/labctl/internal/virt/virtfake"
)

// testLab crée une arborescence Labs minimale avec deux modules.
func testLab(t *testing.T) *Lab {
	t.Helper()
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "config.env"), nil, 0o644)
	for name, yml := range map[string]string{
		"pentest": "module: pentest\nnetwork: {name: pentest-lab, subnet: 10.66.66.0/24}\nvms:\n" +
			"  kali: {ip: 10, mac: \"52:54:00:66:66:10\"}\n  msf2: {ip: 20, mac: \"52:54:00:66:66:20\", internet: never}\n",
		"demo": "module: demo\nnetwork: {name: demo-lab, subnet: 10.77.77.0/24}\nvms: {}\n",
	} {
		dir := filepath.Join(root, "modules", name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "module.yaml"), []byte(yml), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("LABS_PKI_DOMAIN", "omedo.lab")
	return &Lab{Root: root, Virt: virtfake.New()}
}

func TestConfEdits(t *testing.T) {
	l := testLab(t)
	ok := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	bad := func(err error, why string) {
		t.Helper()
		if err == nil {
			t.Errorf("doit échouer : %s", why)
		}
	}
	ok(l.ConfAddHost("pentest", "dc01", 40, "52:54:00:AA:BB:CC"))
	bad(l.ConfAddHost("pentest", "dc02", 40, "52:54:00:aa:bb:dd"), "octet déjà pris")
	bad(l.ConfAddHost("pentest", "dc02", 41, "52:54:00:aa:bb:cc"), "MAC déjà prise (casse ignorée)")
	bad(l.ConfAddHost("pentest", "kali", 42, "52:54:00:aa:bb:ee"), "nom d'une VM du module")
	bad(l.ConfAddHost("pentest", "dc03", 10, "52:54:00:aa:bb:ee"), "octet de kali")
	bad(l.ConfAddHost("pentest", "dc03", 150, "52:54:00:aa:bb:ee"), "octet dans la plage DHCP libre")
	bad(l.ConfAddHost("pentest", "DC03", 43, "52:54:00:aa:bb:ee"), "majuscules")
	bad(l.ConfAddHost("pentest", "dc03", 43, "pas-une-mac"), "MAC invalide")
	bad(l.ConfAddHost("pentest", "dc03", 43, "52:54:00:fe:42:42"), "MAC de la passerelle")

	ok(l.ConfAddAlias("pentest", "intranet", "dc01"))
	ok(l.ConfAddAlias("pentest", "ext", "srv.demo-lab.omedo.lab"))
	ok(l.ConfAddAlias("pentest", "intranet", "kali")) // remplace
	bad(l.ConfAddAlias("pentest", "kali", "dc01"), "alias portant le nom d'une VM")
	bad(l.ConfAddAlias("pentest", "x", "inconnue"), "cible inconnue")

	ok(l.ConfLink("pentest", "demo", true))
	ok(l.ConfLink("pentest", "demo", true)) // idempotent
	bad(l.ConfLink("pentest", "pentest", true), "lien vers soi-même")
	bad(l.ConfLink("pentest", "absent", true), "module inconnu")

	conf := func() *CoreConf {
		t.Helper()
		m, err := l.Module("pentest")
		if err != nil || m.YAMLErr != nil {
			t.Fatalf("module relu : %v %v", err, m.YAMLErr)
		}
		return m.Conf
	}
	c := conf()
	if c.Hosts["dc01"] != 40 || c.HostMACs["dc01"] != "52:54:00:aa:bb:cc" || c.Aliases["intranet"] != "kali" ||
		c.Aliases["ext"] != "srv.demo-lab.omedo.lab" || strings.Join(c.Links, ",") != "demo" {
		t.Errorf("éditions mal enregistrées (doublon ou ancienne valeur) : %+v", c)
	}

	// La configuration générée reflète les éditions
	in, err := l.CoreInput()
	ok(err)
	in.Attached, in.Upstream = []string{"demo", "pentest"}, "192.168.122.1"
	files, err := coreconf.Render(in)
	ok(err)
	if !strings.Contains(files["gwdns/dhcp.conf"], "dhcp-host=52:54:00:aa:bb:cc,10.66.66.40,dc01") || files["gwdns/links"] != "10.66.66.0/24 10.77.77.0/24\n" {
		t.Errorf("config générée :\n%s\n%s", files["gwdns/dhcp.conf"], files["gwdns/links"])
	}

	ok(l.ConfDelHost("pentest", "dc01"))
	bad(l.ConfDelHost("pentest", "kali"), "VM du module, pas une réservation")
	ok(l.ConfDelAlias("pentest", "ext"))
	ok(l.ConfLink("pentest", "demo", false))
	if c := conf(); c.Hosts["dc01"] != 0 || len(c.Links) != 0 || c.Aliases["ext"] != "" {
		t.Errorf("suppressions non faites : %+v", c)
	}
}

// La vue réseau (Env) se déduit du module.yaml : domaine par défaut, IP et MAC des VMs, VMs « never », ordre.
func TestModuleEnvFromYAML(t *testing.T) {
	l := testLab(t)
	m, err := l.Module("pentest")
	if err != nil || m.Env == nil {
		t.Fatalf("module : %v", err)
	}
	e := m.Env
	if e.Domain != "pentest-lab.omedo.lab" {
		t.Errorf("domaine par défaut = %q", e.Domain)
	}
	if e.IP("msf2") != "10.66.66.20" || e.Macs["kali"] != "52:54:00:66:66:10" {
		t.Errorf("IP / MAC : %+v", e)
	}
	if strings.Join(e.NoInternet, ",") != "msf2" || strings.Join(e.VMs(), ",") != "kali,msf2" {
		t.Errorf("internet: never / ordre des VMs : %+v", e)
	}
}

func TestDiffFiles(t *testing.T) {
	have := coreconf.Files{"a": "1\n2\n3\n4\n5\n6\n7\n", "gone": "x\n"}
	want := coreconf.Files{"a": "1\n2\n3\nQUATRE\n5\n6\n7\n", "new": "y\n"}
	ch := DiffFiles(have, want)
	if len(ch) != 3 || ch[0].File != "a" || ch[1].File != "gone" || ch[2].File != "new" {
		t.Fatalf("fichiers changés : %+v", ch)
	}
	var s []string
	for _, d := range ch[0].Lines {
		s = append(s, d.Op+d.Text)
	}
	if strings.Join(s, "|") != "…| 2| 3|-4|+QUATRE| 5| 6|…" {
		t.Errorf("diff : %s", strings.Join(s, "|"))
	}
	if len(DiffFiles(want, want)) != 0 {
		t.Error("aucun changement attendu")
	}
}
