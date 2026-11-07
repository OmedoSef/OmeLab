package lab

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"labs/labctl/internal/virt/virtfake"
)

// Anciens fichiers séparés : lus tant que network.local.yaml n'existe pas, migrés au premier enregistrement.
func writeLegacy(t *testing.T, dir string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "attach.local.yaml"), []byte("attach:\n  - {on: pentest, vm: attacker/kali, ip: 10}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "connections.local.yaml"), []byte("connections:\n  - {on: netlab, to: internet, via: proxy}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestNetworkLocalMigration(t *testing.T) {
	dir := t.TempDir()
	writeLegacy(t, dir)
	l := &Lab{Root: dir, Virt: virtfake.New()}
	f, err := l.loadNetworkLocal()
	if err != nil || len(f.Attach) != 1 || len(f.Connections) != 1 {
		t.Fatalf("lecture des anciens fichiers : %+v %v", f, err)
	}
	if err := l.saveNetworkLocal(f); err != nil {
		t.Fatal(err)
	}
	for _, old := range []string{"attach.local.yaml", "connections.local.yaml"} {
		if _, err := os.Stat(filepath.Join(dir, old)); !os.IsNotExist(err) {
			t.Errorf("%s devrait avoir été retiré après migration", old)
		}
	}
	g, err := l.loadNetworkLocal()
	if err != nil || len(g.Attach) != 1 || len(g.Connections) != 1 {
		t.Fatalf("relecture de network.local.yaml : %+v %v", g, err)
	}
	// Aucun fichier temporaire ne traîne
	if m, _ := filepath.Glob(filepath.Join(dir, ".labctl-*")); len(m) > 0 {
		t.Errorf("temporaires restants : %v", m)
	}
}

// Si l'écriture échoue, les anciens fichiers restent : le câblage local n'est jamais perdu.
func TestNetworkLocalWriteFailureKeepsLegacy(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignore les droits du dossier")
	}
	dir := t.TempDir()
	writeLegacy(t, dir)
	l := &Lab{Root: dir, Virt: virtfake.New()}
	f, err := l.loadNetworkLocal()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o500); err != nil { // dossier en lecture seule : écriture impossible
		t.Fatal(err)
	}
	defer os.Chmod(dir, 0o700)
	if err := l.saveNetworkLocal(f); err == nil {
		t.Fatal("l'écriture aurait dû échouer")
	}
	os.Chmod(dir, 0o700)
	for _, old := range []string{"attach.local.yaml", "connections.local.yaml"} {
		if _, err := os.Stat(filepath.Join(dir, old)); err != nil {
			t.Errorf("%s perdu après un échec d'écriture : %v", old, err)
		}
	}
}

// Un network.local.yaml mal formé ne doit jamais devenir un câblage vide appliqué en silence : la configuration de
// la passerelle refuse de se générer, et une écriture ne l'écrase pas.
func TestNetworkLocalMalformedBlocksApply(t *testing.T) {
	l := vulnerableLab(t)                                                   // un lab avec des modules
	broken := []byte("attach:\n  - {on: good, vm: attacker/kali, ip: 10\n") // accolade non fermée
	p := filepath.Join(l.Root, "network.local.yaml")
	if err := os.WriteFile(p, broken, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := l.CoreInput(); err == nil || !strings.Contains(err.Error(), "network.local.yaml illisible") {
		t.Fatalf("CoreInput devrait refuser : %v", err)
	}
	if err := l.AddLocalAttach("good", "attacker/kali", 10); err == nil {
		t.Fatal("l'écriture aurait dû échouer sur un fichier illisible")
	}
	if b, _ := os.ReadFile(p); string(b) != string(broken) {
		t.Fatal("le fichier illisible a été écrasé")
	}
}
