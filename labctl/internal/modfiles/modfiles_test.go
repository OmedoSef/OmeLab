package modfiles

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Aucun chemin ne sort de files/ : « .. », chemin absolu, octet nul, et aucun lien symbolique n'est suivi — ni le
// dossier files/ lui-même, ni un dossier intermédiaire, ni le fichier.
func TestConfinement(t *testing.T) {
	for _, rel := range []string{"", "/etc/passwd", "../x", "a/../../x", "..", ".", "a\x00b", strings.Repeat("a", 256)} {
		if _, err := cleanRel(rel); err == nil {
			t.Errorf("cleanRel(%q) accepté", rel)
		}
	}
	if c, err := cleanRel(` conf\sub/./app.ini `); err != nil || c != "conf/sub/app.ini" {
		t.Errorf("chemin nettoyé : %q %v", c, err)
	}

	root := t.TempDir()
	secret := filepath.Join(root, "secret")
	_ = os.WriteFile(secret, []byte("SECRET"), 0o600)
	tree := Tree{Base: filepath.Join(root, "files")}
	_ = os.MkdirAll(tree.Base, 0o755)
	_ = os.Symlink(root, filepath.Join(tree.Base, "dehors"))     // dossier intermédiaire
	_ = os.Symlink(secret, filepath.Join(tree.Base, "lien.txt")) // fichier
	for _, rel := range []string{"dehors/secret", "lien.txt"} {
		if _, _, err := tree.Text(rel); err == nil {
			t.Errorf("lecture de %s par un lien", rel)
		}
		if _, err := tree.SaveText(rel, "x", "", true); err == nil {
			t.Errorf("écriture de %s par un lien", rel)
		}
	}
	if b, _ := os.ReadFile(secret); string(b) != "SECRET" {
		t.Fatal("fichier hors de files/ modifié")
	}
	// files/ lui-même est un lien : tout est refusé
	link := Tree{Base: filepath.Join(root, "files2")}
	_ = os.Symlink(root, link.Base)
	if _, _, err := link.Text("secret"); err == nil {
		t.Error("files/ en lien symbolique suivi")
	}
	// Un lien n'est pas supprimé non plus (refusé comme tout chemin qui passe par un lien) ; sa cible reste
	if err := tree.Delete("lien.txt"); err == nil || !exists(secret) {
		t.Errorf("suppression par un lien : %v, cible présente %v", err, exists(secret))
	}
}

// Enregistrement : conflit si le fichier a changé depuis sa lecture, création refusée s'il existe, écriture atomique.
func TestSaveTextAndUpload(t *testing.T) {
	tree := Tree{Base: t.TempDir()}
	h, err := tree.SaveText("a.conf", "v=1\n", "", true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tree.SaveText("a.conf", "v=1\n", "", true); !errors.Is(err, ErrExists) {
		t.Errorf("création d'un fichier existant : %v", err)
	}
	if _, err := tree.SaveText("a.conf", "v=2\n", "périmée", false); !errors.Is(err, ErrConflict) {
		t.Errorf("version périmée : %v", err)
	}
	if _, err := tree.SaveText("a.conf", "v=2\n", h, false); err != nil {
		t.Fatal(err)
	}
	if err := tree.Upload("gros.bin", strings.NewReader(strings.Repeat("x", 10)), false); err != nil {
		t.Fatal(err)
	}
	if err := WriteAtomic(filepath.Join(tree.Base, "trop.bin"), strings.NewReader(strings.Repeat("x", 11)), 10); err == nil || exists(filepath.Join(tree.Base, "trop.bin")) {
		t.Errorf("fichier au-delà de la limite : %v", err)
	}
	if err := tree.Rename("a.conf", "sous/a.conf"); err != nil {
		t.Fatal(err)
	}
	if err := tree.Rename("sous", "sous/plus/bas"); err == nil {
		t.Error("dossier déplacé dans lui-même")
	}
}

func exists(p string) bool { _, err := os.Lstat(p); return err == nil }

// List : fichiers et dossiers de files/, reliés aux entrées files: qui les copient ; entrées dont le fichier manque ;
// liens ni listés ni suivis.
func TestList(t *testing.T) {
	tree := Tree{Base: t.TempDir()}
	if err := tree.Mkdir("conf"); err != nil {
		t.Fatal(err)
	}
	_, _ = tree.SaveText("conf/app.ini", "a=1\n", "", true)
	_ = os.WriteFile(filepath.Join(tree.Base, "image.bin"), []byte{0, 1, 2}, 0o644)
	_ = os.Symlink("/etc", filepath.Join(tree.Base, "lien"))
	uses := map[string][]FileRef{
		"conf/app.ini": {{VM: "srv", Src: "files/conf/app.ini", Dest: "/etc/app.ini"}},
		"absent.conf":  {{VM: "srv", Src: "files/absent.conf", Dest: "/etc/absent"}},
	}
	files, missing, err := tree.List(uses)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]File{}
	for _, f := range files {
		got[f.Path] = f
	}
	if len(files) != 3 || !got["conf"].Dir || !got["conf/app.ini"].Text || got["image.bin"].Text || len(got["conf/app.ini"].Uses) != 1 {
		t.Errorf("liste : %+v", files)
	}
	if _, ok := got["lien"]; ok {
		t.Error("lien listé")
	}
	if len(missing) != 1 || missing[0].Src != "files/absent.conf" {
		t.Errorf("manquants : %+v", missing)
	}
	// files/ absent : rien à lister, les références sont manquantes
	none := Tree{Base: filepath.Join(t.TempDir(), "files")}
	if files, missing, err := none.List(uses); err != nil || len(files) != 0 || len(missing) != 2 {
		t.Errorf("files/ absent : %v %v %v", files, missing, err)
	}
}
