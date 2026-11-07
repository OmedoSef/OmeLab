package lab

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Lab de test : un module vulnérable dont le module.yaml a été modifié à la main (connexion et lien sortants,
// jamais validés), et un module ordinaire.
func vulnerableLab(t *testing.T) *Lab {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "config.env"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	write := func(name, yml string) {
		dir := filepath.Join(root, "modules", name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "module.yaml"), []byte(yml), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("vuln", "module: vuln\nvulnerable: true\nnetwork: {subnet: 10.80.80.0/24}\ndns: {links: [good]}\n"+
		"connections:\n  - {to: internet}\nvms:\n  web: {ip: 30}\n")
	write("good", "module: good\nnetwork: {subnet: 10.81.81.0/24}\nvms:\n  srv: {ip: 10}\n")
	t.Setenv("LABS_PKI_DOMAIN", "omelab.internal")
	return &Lab{Root: root}
}

// « core internet on » est refusé pour un module vulnérable, avant tout accès à la passerelle.
func TestInternetOnRefusedForVulnerable(t *testing.T) {
	l := vulnerableLab(t)
	for _, vms := range [][]string{nil, {"web"}} {
		err := l.Gateway(io.Discard).InternetOn("vuln", vms)
		if err == nil || !strings.Contains(err.Error(), "module vulnérable") {
			t.Errorf("internet on vuln %v : refus attendu, obtenu %v", vms, err)
		}
	}
}

// Une VM d'un module vulnérable ne peut pas être branchée (câblage local) sur un autre module.
func TestAttachRefusedForVulnerableVM(t *testing.T) {
	l := vulnerableLab(t)
	if _, err := l.CheckAttach("good", "vuln/web", 0); err == nil || !strings.Contains(err.Error(), "module vulnérable") {
		t.Errorf("branchement de vuln/web sur good : refus attendu, obtenu %v", err)
	}
}

// Même modifié à la main, un module vulnérable ne produit aucune règle sortante sur la passerelle.
func TestCoreInputDropsVulnerableOutgoing(t *testing.T) {
	l := vulnerableLab(t)
	in, err := l.CoreInput()
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range in.Modules {
		if m.Name == "vuln" && (len(m.Conns) > 0 || len(m.Links) > 0) {
			t.Fatalf("règles sortantes générées pour vuln : %+v %+v", m.Conns, m.Links)
		}
	}
	if !strings.Contains(strings.Join(in.Skipped, " "), "vuln vulnérable") {
		t.Errorf("avertissement attendu : %v", in.Skipped)
	}
}
