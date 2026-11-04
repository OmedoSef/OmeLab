package lab

import (
	"os"
	"path/filepath"
	"testing"
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
	l := &Lab{Root: root}
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
