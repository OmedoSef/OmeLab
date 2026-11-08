package module

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Un module tiers qui livre module.yaml.tmp (ou tout nom temporaire) en lien vers un fichier de l'hôte ne le fait
// pas écrire quand labctl réécrit son module.yaml ; un module.yaml qui est lui-même un lien est refusé.
func TestSaveNeverFollowsSymlinks(t *testing.T) {
	m := pentestCopy(t)
	victim := filepath.Join(t.TempDir(), "bashrc")
	if err := os.WriteFile(victim, []byte("# à moi\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, m.Path+".tmp"); err != nil {
		t.Fatal(err)
	}
	if err := m.SetVMValue("web", "cpus", "3"); err != nil {
		t.Fatal(err)
	}
	if err := m.Save(); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(victim); string(b) != "# à moi\n" {
		t.Fatalf("fichier hors du module écrit par un lien : %q", b)
	}
	if fi, err := os.Lstat(m.Path); err != nil || !fi.Mode().IsRegular() {
		t.Fatalf("module.yaml n'est plus un fichier ordinaire : %v %v", fi, err)
	}

	// module.yaml en lien : ni lu, ni écrit
	other := filepath.Join(t.TempDir(), "autre")
	_ = os.MkdirAll(other, 0o755)
	if err := os.Symlink(victim, filepath.Join(other, FileName)); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(other); err == nil || !strings.Contains(err.Error(), "lien symbolique") {
		t.Errorf("module.yaml en lien lu : %v", err)
	}
	m.Path = filepath.Join(other, FileName)
	if err := m.Save(); err == nil {
		t.Error("écriture d'un module.yaml en lien")
	}
	if b, _ := os.ReadFile(victim); string(b) != "# à moi\n" {
		t.Fatalf("cible du lien modifiée : %q", b)
	}
}
