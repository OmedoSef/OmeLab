package lab

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

var usageLines = []string{
	"# Module demo : test",
	"#",
	"#   ./lab.sh demo start|stop [VM]          démarre / arrête",
	"#   ./lab.sh demo snapshot VM [NOM]        snapshot",
	"#   ./lab.sh demo restore VM [NOM]         restaure",
	"#   ./lab.sh demo internet on MODULE [VM...]   ouvre",
	"#   ./lab.sh demo link A B [--both]        lien",
	"#   ./lab.sh demo status | ssh [cmd] | destroy",
	"#   ./lab.sh autre ignore                  autre module : ignoré",
}

type fakeCompleter struct{}

func (fakeCompleter) ModuleNames() []string { return []string{"demo", "pentest"} }
func (fakeCompleter) VMs(m string) []string {
	if m == "pentest" {
		return []string{"kali", "msf2"}
	}
	return []string{"a", "b"}
}
func (fakeCompleter) Snapshots(m, vm string) []string { return []string{"clean-" + m + "-" + vm} }

func TestParseUsageTree(t *testing.T) {
	root := ParseUsage("demo", usageLines)
	want := []string{"destroy", "internet", "link", "restore", "snapshot", "ssh", "start", "status", "stop"}
	got := root.Complete("demo", nil, "", fakeCompleter{})
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("commandes = %v, attendu %v", got, want)
	}
	if _, ok := root.Children["autre"]; ok {
		t.Error("une ligne d'un autre module ne doit pas être prise en compte")
	}
}

func TestComplete(t *testing.T) {
	root := ParseUsage("demo", usageLines)
	cases := []struct {
		typed []string
		cur   string
		want  []string
	}{
		{[]string{"start"}, "", []string{"a", "b"}},                           // VM du module courant
		{[]string{"restore", "a"}, "", []string{"clean-demo-a"}},              // NOM après VM = snapshots
		{[]string{"internet"}, "", []string{"on"}},                            // sous-commande
		{[]string{"internet", "on"}, "", []string{"demo", "pentest"}},         // MODULE
		{[]string{"internet", "on", "pentest"}, "", []string{"kali", "msf2"}}, // VM du module saisi
		{[]string{"internet", "on", "pentest", "kali"}, "", []string{"msf2"}}, // variadique, sans doublon
		{[]string{"link", "demo"}, "", []string{"pentest"}},                   // B : pas le module déjà saisi
		{[]string{"link", "demo", "pentest"}, "", []string{"--both"}},         // option finale
		{[]string{"start"}, "b", []string{"b"}},                               // filtrage par préfixe
		{[]string{"status"}, "", nil},                                         // pas d'argument
	}
	for _, c := range cases {
		got := root.Complete("demo", c.typed, c.cur, fakeCompleter{})
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("Complete(%v, %q) = %v, attendu %v", c.typed, c.cur, got, c.want)
		}
	}
}

func TestParseEnvAndCoreConf(t *testing.T) {
	dir := t.TempDir()
	env := filepath.Join(dir, "module.env")
	os.WriteFile(env, []byte(`# shellcheck shell=bash
MODULE=pentest
NET=pentest-lab
SUBNET=10.66.66
declare -A HOSTS=([kali]=10 [msf2]=20)
declare -A MACS=([kali]=52:54:00:66:66:10 [msf2]=52:54:00:66:66:20)
NO_INTERNET="msf2"
`), 0o644)
	e, err := ParseEnv(env, "omedo.lab")
	if err != nil {
		t.Fatal(err)
	}
	if e.Domain != "pentest-lab.omedo.lab" {
		t.Errorf("domaine par défaut = %q", e.Domain)
	}
	if e.IP("msf2") != "10.66.66.20" || e.Macs["kali"] != "52:54:00:66:66:10" {
		t.Errorf("hosts/macs mal lus : %+v", e)
	}
	if !reflect.DeepEqual(e.NoInternet, []string{"msf2"}) || !reflect.DeepEqual(e.VMs(), []string{"kali", "msf2"}) {
		t.Errorf("NO_INTERNET / ordre des VMs : %+v", e)
	}

	conf := filepath.Join(dir, "core.conf")
	os.WriteFile(conf, []byte("# commentaire\nhost dc01 40 52:54:00:aa:bb:cc\nalias dvwa web\nlink demo\n"), 0o644)
	c, err := ParseCoreConf(conf)
	if err != nil {
		t.Fatal(err)
	}
	if c.Hosts["dc01"] != 40 || c.Aliases["dvwa"] != "web" || !reflect.DeepEqual(c.Links, []string{"demo"}) {
		t.Errorf("core.conf mal lu : %+v", c)
	}
	if empty, err := ParseCoreConf(filepath.Join(dir, "absent")); err != nil || len(empty.Links) != 0 {
		t.Errorf("core.conf absent : %+v, %v", empty, err)
	}
}
