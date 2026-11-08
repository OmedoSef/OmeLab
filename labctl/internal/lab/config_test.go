package lab

import (
	"os"
	"path/filepath"
	"sync"
	"testing"

	"labs/labctl/internal/virt/virtfake"
)

func TestSettingExpansion(t *testing.T) {
	root := t.TempDir()

	os.WriteFile(filepath.Join(root, "config.env"), []byte(`LABS_STORAGE_DIR="${LABS_STORAGE_DIR:-$HOME/Virt/kvm}"
LABS_IMAGES_DIR="${LABS_IMAGES_DIR:-$LABS_STORAGE_DIR/images}"
LABS_PKI_DIR="${LABS_PKI_DIR:-$(cd x && pwd)/pki}"
LABS_VM_USER="${LABS_VM_USER:-lab}"
`), 0o644)
	for _, v := range []string{"LABS_STORAGE_DIR", "LABS_IMAGES_DIR", "LABS_PKI_DIR", "LABS_VM_USER"} {
		t.Setenv(v, "")
	}
	l := &Lab{Root: root, Virt: virtfake.New()}
	home, _ := os.UserHomeDir()
	if got := l.Setting("LABS_IMAGES_DIR", ""); got != home+"/Virt/kvm/images" {
		t.Errorf("développement imbriqué : %q", got)
	}
	if got := l.Setting("LABS_PKI_DIR", "repli"); got != "repli" {
		t.Errorf("$(...) doit donner le repli : %q", got)
	}
	os.WriteFile(filepath.Join(root, "config.local.env"), []byte("LABS_STORAGE_DIR=/data/kvm\n"), 0o644)
	if got := l.Setting("LABS_IMAGES_DIR", ""); got != "/data/kvm/images" {
		t.Errorf("config.local.env doit primer : %q", got)
	}
	t.Setenv("LABS_STORAGE_DIR", "/env/kvm")
	if got := l.Setting("LABS_IMAGES_DIR", ""); got != "/env/kvm/images" {
		t.Errorf("l'environnement doit primer : %q", got)
	}
}

// Les variables du lab suivent lab.yaml sans redémarrer (la GUI garde un même Lab), et se lisent en parallèle.
func TestLabVarsFollowFileAndConcurrent(t *testing.T) {
	l := &Lab{Root: t.TempDir(), Virt: virtfake.New()}
	write := func(s string) {
		if err := os.WriteFile(filepath.Join(l.Root, "lab.yaml"), []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("vars: {iso: /a.iso}\n")
	if got := l.LabVars()["iso"]; got != "/a.iso" {
		t.Fatalf("iso : %q", got)
	}
	write("vars: {iso: /autre.iso}\n") // taille différente : changement vu même à date identique
	if got := l.LabVars()["iso"]; got != "/autre.iso" {
		t.Fatalf("lab.yaml modifié non relu : %q", got)
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() { defer wg.Done(); _ = l.LabVars() }()
	}
	wg.Wait()
}
