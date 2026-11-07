package module

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSourcePath(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "mod")
	if err := os.MkdirAll(filepath.Join(dir, "files"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "files", "ok.conf"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(root, "secret.key") // hors du module : ne doit jamais partir dans une VM
	if err := os.WriteFile(secret, []byte("PRIVATE KEY"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(dir, "files", "lien.key")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(root, filepath.Join(dir, "sortie")); err != nil {
		t.Fatal(err)
	}

	for _, bad := range []string{
		secret, "/etc/passwd", "~/.ssh/id_ed25519", "../secret.key", "files/../../secret.key",
		`..\secret.key`, "C:/Windows/x", "", "files/lien.key", "sortie/secret.key",
	} {
		if p, err := SourcePath(dir, bad); err == nil {
			t.Errorf("src %q accepté (-> %s)", bad, p)
		}
	}
	p, err := SourcePath(dir, "files/ok.conf")
	if err != nil || p != filepath.Join(dir, "files", "ok.conf") {
		t.Fatalf("src légitime refusé : %q %v", p, err)
	}

	// Le dossier du module peut lui-même être un lien (module développé ailleurs)
	linked := filepath.Join(root, "modules-lien")
	if err := os.Symlink(dir, linked); err != nil {
		t.Fatal(err)
	}
	if _, err := SourcePath(linked, "files/ok.conf"); err != nil {
		t.Fatalf("dossier de module relié refusé : %v", err)
	}
}

// Bout en bout : la validation d'un module.yaml signale un src qui vise un fichier de l'hôte.
func TestValidateRejectsHostFileSource(t *testing.T) {
	dir := t.TempDir()
	src := "module: m\nvms:\n  srv:\n    files:\n      - {src: /etc/passwd, dest: /tmp/x}\n      - {src: ../../pki/root/root-ca.key, dest: /tmp/y}\n"
	m, err := Parse([]byte(src), dir)
	if err != nil {
		t.Fatal(err)
	}
	m.Module = filepath.Base(dir)
	var found int
	for _, i := range m.Validate(Context{RootDomain: "omelab.internal"}) {
		if i.Error && strings.HasSuffix(i.Path, ".src") {
			found++
		}
	}
	if found != 2 {
		t.Fatalf("attendu 2 erreurs sur files.src, obtenu %d", found)
	}
}
