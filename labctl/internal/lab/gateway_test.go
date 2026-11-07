package lab

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
