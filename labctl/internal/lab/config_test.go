package lab

import (
	"os"
	"path/filepath"
	"strings"
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

// Recherche du lab : LABS_ROOT, puis le lab du dossier courant, puis le lab par défaut du registre (labctl init /
// use) ; un lab par défaut disparu ou un dossier qui n'est pas un lab sont des erreurs qui disent quoi faire.
func TestLocate(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("LABS_ROOT", "")
	os.Unsetenv("LABS_ROOT")
	mk := func(name string) string {
		d := filepath.Join(t.TempDir(), name)
		_ = os.MkdirAll(filepath.Join(d, "modules", "x"), 0o755)
		_ = os.WriteFile(filepath.Join(d, "config.env"), nil, 0o644)
		return d
	}
	a, b, ailleurs := mk("Perso Lab"), mk("formation"), t.TempDir()
	t.Chdir(ailleurs)
	if _, _, err := Locate(); err == nil || !strings.Contains(err.Error(), "labctl use") {
		t.Fatalf("sans lab : %v", err)
	}
	reg, _ := LoadRegistry()
	if _, err := reg.Add("", ailleurs); err == nil {
		t.Error("dossier qui n'est pas un lab enregistré")
	}
	na, err := reg.Add("", a)
	if err != nil || na != "perso-lab" {
		t.Fatalf("nom tiré du dossier : %q %v", na, err)
	}
	if _, err := reg.Add("perso-lab", b); err == nil {
		t.Error("nom déjà pris accepté pour un autre dossier")
	}
	nb, _ := reg.Add("", b)
	reg.Default = na
	if err := reg.Save(); err != nil {
		t.Fatal(err)
	}
	if l, how, err := Locate(); err != nil || l.Root != a || !strings.Contains(how, "perso-lab") {
		t.Errorf("depuis ailleurs : %v %q %v", l, how, err)
	}
	t.Chdir(filepath.Join(b, "modules", "x")) // le lab du dossier courant l'emporte sur le lab par défaut
	if l, how, _ := Locate(); l == nil || l.Root != b || how != "dossier courant" {
		t.Errorf("dans un autre lab : %v %q", l, how)
	}
	t.Setenv("LABS_ROOT", a) // LABS_ROOT l'emporte sur tout
	if l, _, _ := Locate(); l == nil || l.Root != a {
		t.Errorf("LABS_ROOT : %v", l)
	}
	os.Unsetenv("LABS_ROOT")
	t.Chdir(ailleurs)
	reloaded, _ := LoadRegistry() // relu depuis le fichier
	if reloaded.Default != na || reloaded.Labs[nb] != b {
		t.Errorf("registre relu : %+v", reloaded)
	}
	if n, _ := reloaded.Add("perso", a); n != "perso" || reloaded.Default != "perso" || reloaded.NameOf(a) != "perso" {
		t.Errorf("renommage : %q, défaut %q", n, reloaded.Default)
	}
	_ = reloaded.Save()
	_ = os.RemoveAll(a)
	if _, _, err := Locate(); err == nil || !strings.Contains(err.Error(), "n'existe plus") {
		t.Errorf("lab par défaut disparu : %v", err)
	}
	if err := reloaded.Forget("perso"); err != nil || reloaded.Default != "" {
		t.Errorf("oubli du lab par défaut : %v, défaut %q", err, reloaded.Default)
	}
}
