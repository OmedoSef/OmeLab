package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"labs/labctl/internal/lab"
)

// settings relit l'organisation et le domaine d'un lab créé par runInit, comme labctl le fera ensuite.
func settings(t *testing.T, dir string) (org, domain string) {
	t.Helper()
	t.Setenv("LABS_PKI_ORG", "")
	os.Unsetenv("LABS_PKI_ORG")
	t.Setenv("LABS_PKI_DOMAIN", "")
	os.Unsetenv("LABS_PKI_DOMAIN")
	l := &lab.Lab{Root: dir}
	return l.Org(), l.RootDomain()
}

func TestInitDefaults(t *testing.T) {
	dir := t.TempDir()
	if err := runInit(&bytes.Buffer{}, strings.NewReader(""), dir, false, identity{org: lab.DefaultOrg, domain: lab.DefaultDomain}); err != nil {
		t.Fatal(err)
	}
	if org, dom := settings(t, dir); org != "OmeLab" || dom != "omelab.internal" {
		t.Fatalf("défauts : %q %q", org, dom)
	}
}

func TestInitFlags(t *testing.T) {
	dir := t.TempDir()
	if err := runInit(&bytes.Buffer{}, strings.NewReader(""), dir, false, identity{org: "ACME Lab", domain: "ACME.Internal"}); err != nil {
		t.Fatal(err)
	}
	if org, dom := settings(t, dir); org != "ACME Lab" || dom != "acme.internal" {
		t.Fatalf("options : %q %q", org, dom)
	}
	// Valeurs refusées
	for _, id := range []identity{{org: "", domain: "a.internal"}, {org: `x"y`, domain: "a.internal"}, {org: "ok", domain: "sans_point"}} {
		if err := runInit(&bytes.Buffer{}, strings.NewReader(""), t.TempDir(), false, id); err == nil {
			t.Errorf("devrait refuser %+v", id)
		}
	}
}

func TestInitQuestions(t *testing.T) {
	dir := t.TempDir()
	// Organisation saisie ; domaine : une réponse refusée, puis Entrée (défaut)
	in := strings.NewReader("Mon Lab\nmauvais_domaine\n\n")
	var out bytes.Buffer
	id := identity{org: lab.DefaultOrg, domain: lab.DefaultDomain, askOrg: true, askDomain: true}
	if err := runInit(&out, in, dir, false, id); err != nil {
		t.Fatal(err)
	}
	if org, dom := settings(t, dir); org != "Mon Lab" || dom != "omelab.internal" {
		t.Fatalf("questions : %q %q\n%s", org, dom, out.String())
	}
	if !strings.Contains(out.String(), "domaine invalide") {
		t.Errorf("la réponse refusée devrait être signalée :\n%s", out.String())
	}
}

func TestInitKeepsExistingConfig(t *testing.T) {
	dir := t.TempDir()
	if err := runInit(&bytes.Buffer{}, strings.NewReader(""), dir, false, identity{org: "Premier", domain: "premier.internal"}); err != nil {
		t.Fatal(err)
	}
	// --force sur un lab existant : config.env n'est ni réécrit ni redemandé
	id := identity{org: "Second", domain: "second.internal", askOrg: true, askDomain: true}
	if err := runInit(&bytes.Buffer{}, strings.NewReader(""), dir, true, id); err != nil {
		t.Fatal(err)
	}
	if org, _ := settings(t, dir); org != "Premier" {
		t.Fatalf("config.env existant modifié : %q", org)
	}
	if _, err := os.Stat(filepath.Join(dir, "modules", ".gitkeep")); err != nil {
		t.Fatal(err)
	}
}
