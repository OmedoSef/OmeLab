package lab

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestModuleFiles(t *testing.T) {
	l := testLab(t)
	if _, _, err := l.SaveModuleText("fic", ModuleSkeleton("fic"), "", true); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(l.Root, "modules", "fic")

	// Création, lecture, conflit, envoi sans écrasement
	h, err := l.SaveModuleFileText("fic", "conf/app.ini", "a=1\n", "", true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := l.SaveModuleFileText("fic", "conf/app.ini", "x", "", true); !errors.Is(err, ErrExists) {
		t.Errorf("création d'un fichier existant : %v", err)
	}
	if _, err := l.SaveModuleFileText("fic", "conf/app.ini", "a=2\n", "mauvaise", false); !errors.Is(err, ErrConflict) {
		t.Errorf("conflit non détecté : %v", err)
	}
	if _, err := l.SaveModuleFileText("fic", "conf/app.ini", "a=2\n", h, false); err != nil {
		t.Fatal(err)
	}
	if txt, _, err := l.ModuleFileText("fic", "conf/app.ini"); err != nil || txt != "a=2\n" {
		t.Errorf("relu %q, %v", txt, err)
	}
	if err := l.UploadModuleFile("fic", "bin.dat", strings.NewReader("\x00\x01"), false); err != nil {
		t.Fatal(err)
	}
	if err := l.UploadModuleFile("fic", "bin.dat", strings.NewReader("z"), false); !errors.Is(err, ErrExists) {
		t.Errorf("écrasement sans overwrite : %v", err)
	}
	if _, _, err := l.ModuleFileText("fic", "bin.dat"); err == nil {
		t.Error("binaire ouvert comme texte")
	}

	// Rien hors de files/ : .., chemin absolu, lien symbolique (fichier ou dossier)
	outside := filepath.Join(l.Root, "secret")
	os.WriteFile(outside, []byte("s"), 0o600)
	os.Symlink(outside, filepath.Join(dir, "files", "lien"))
	os.Symlink(l.Root, filepath.Join(dir, "files", "dlien"))
	for _, bad := range []string{"../module.yaml", "a/../../x", "/etc/passwd", "lien", "dlien/secret", "", "."} {
		if _, _, err := l.ModuleFileText("fic", bad); err == nil {
			t.Errorf("lecture de %q acceptée", bad)
		}
		if err := l.UploadModuleFile("fic", bad, strings.NewReader("x"), true); err == nil {
			t.Errorf("écriture de %q acceptée", bad)
		}
	}
	if err := l.RenameModuleFile("fic", "bin.dat", "../bin.dat"); err == nil {
		t.Error("déplacement hors de files/ accepté")
	}
	if err := l.DeleteModuleFile("fic", "dlien/secret"); err == nil {
		t.Error("suppression à travers un lien acceptée")
	}
	if b, _ := os.ReadFile(outside); string(b) != "s" {
		t.Error("fichier hors de files/ modifié")
	}

	// Liste : liens ignorés, fichiers texte repérés
	fl, err := l.ModuleFiles("fic")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]ModuleFile{}
	for _, f := range fl.Files {
		got[f.Path] = f
	}
	if _, ok := got["lien"]; ok {
		t.Error("lien symbolique listé")
	}
	if !got["conf/app.ini"].Text || got["bin.dat"].Text || !got["conf"].Dir {
		t.Errorf("liste : %+v", fl.Files)
	}

	// Renommage, suppression d'un dossier, déplacement dans lui-même refusé
	if err := l.RenameModuleFile("fic", "conf", "conf/sous"); err == nil {
		t.Error("dossier déplacé dans lui-même")
	}
	if err := l.RenameModuleFile("fic", "conf/app.ini", "etc/app.ini"); err != nil {
		t.Fatal(err)
	}
	if err := l.DeleteModuleFile("fic", "etc"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "files", "etc")); err == nil {
		t.Error("dossier non supprimé")
	}
	if err := l.MkdirModuleFile("fic", "vide/sous"); err != nil {
		t.Fatal(err)
	}

	// Références du module.yaml : fichier utilisé, fichier absent
	yml := strings.Replace(ModuleSkeleton("fic"), "    # files:\n    #   - {src: files/index.html,", "    files:\n      - {src: files/web.conf, dest: /etc/web.conf}\n      - {src: files/index.html,", 1)
	if !strings.Contains(yml, "src: files/web.conf") {
		t.Fatal("squelette : section files introuvable")
	}
	os.WriteFile(filepath.Join(dir, "module.yaml"), []byte(yml), 0o644)
	if _, err := l.SaveModuleFileText("fic", "web.conf", "x", "", true); err != nil {
		t.Fatal(err)
	}
	fl, err = l.ModuleFiles("fic")
	if err != nil || fl.Warning != "" {
		t.Fatal(err, fl.Warning)
	}
	for _, f := range fl.Files {
		if f.Path == "web.conf" && (len(f.Uses) != 1 || f.Uses[0].Dest != "/etc/web.conf") {
			t.Errorf("utilisation de web.conf : %+v", f.Uses)
		}
	}
	if len(fl.Missing) != 1 || fl.Missing[0].Src != "files/index.html" || !fl.Missing[0].Template {
		t.Errorf("absents : %+v", fl.Missing)
	}
}
