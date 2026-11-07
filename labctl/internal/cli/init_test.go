package cli

import (
	"bytes"
	"os"
	"os/exec"
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

// labctl init crée une clé SSH propre au lab, privée, exclue de git, que labctl utilise ensuite — même si le
// dossier du lab est déplacé ($LABS_ROOT).
func TestInitCreatesLabSSHKey(t *testing.T) {
	if _, err := exec.LookPath("ssh-keygen"); err != nil {
		t.Skip("ssh-keygen absent")
	}
	t.Setenv("LABS_SSH_PUBKEY", "")
	os.Unsetenv("LABS_SSH_PUBKEY")
	dir := t.TempDir()
	if err := runInit(&bytes.Buffer{}, strings.NewReader(""), dir, false, identity{org: lab.DefaultOrg, domain: lab.DefaultDomain}); err != nil {
		t.Fatal(err)
	}
	priv := filepath.Join(dir, ".ssh", "id_ed25519")
	if fi, err := os.Stat(priv); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("clé privée : %v %v", fi, err)
	}
	if fi, _ := os.Stat(filepath.Join(dir, ".ssh")); fi.Mode().Perm() != 0o700 {
		t.Errorf("dossier .ssh en %o", fi.Mode().Perm())
	}
	if gi, _ := os.ReadFile(filepath.Join(dir, ".gitignore")); !strings.Contains(string(gi), "/.ssh/") {
		t.Error(".ssh/ absent du .gitignore")
	}
	// Le lab déplacé garde sa clé : le chemin est relatif à sa racine
	moved := filepath.Join(t.TempDir(), "ailleurs")
	if err := os.Rename(dir, moved); err != nil {
		t.Fatal(err)
	}
	l := &lab.Lab{Root: moved}
	if got := l.SSHPubKey(); got != filepath.Join(moved, ".ssh", "id_ed25519.pub") {
		t.Errorf("clé publique : %q", got)
	}
	if got := l.SSHIdentity(); got != filepath.Join(moved, ".ssh", "id_ed25519") {
		t.Errorf("clé présentée : %q", got)
	}
}
