package lab

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"labs/labctl/internal/modfiles"
	"labs/labctl/internal/module"
)

func TestModuleTextEdit(t *testing.T) {
	l := testLab(t)

	// Squelette : valide tel quel (avertissement « pas de sous-réseau » seulement)
	sk := ModuleSkeleton("neuf")
	if issues := l.CheckModuleText("neuf", sk); module.Errors(issues) != 0 {
		t.Fatalf("squelette invalide : %v", issues)
	}

	// Erreur de lecture : placée à sa ligne
	issues := l.CheckModuleText("neuf", "module: neuf\nvms:\n  srv:\n    cpu: 2\n")
	if len(issues) != 1 || issues[0].Line != 4 || !issues[0].Error {
		t.Fatalf("clé inconnue : %+v", issues)
	}

	// Création, puis refus d'un nom déjà pris ou réservé
	h, _, err := l.SaveModuleText("neuf", sk, "", true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(l.Root, "modules", "neuf", "files")); err != nil {
		t.Error("dossier files/ non créé")
	}
	if _, _, err := l.SaveModuleText("neuf", sk, "", true); err == nil {
		t.Error("création d'un module existant acceptée")
	}
	if _, _, err := l.SaveModuleText("validate", sk, "", true); err == nil {
		t.Error("nom réservé accepté")
	}

	// Lecture et empreinte
	text, hash, err := l.ModuleText("neuf")
	if err != nil || text != sk || hash != h {
		t.Fatalf("relecture : %v %q", err, hash)
	}

	// Texte invalide : refusé, fichier intact
	bad := strings.Replace(sk, "ip: 10", "ip: 150", 1)
	if _, issues, err := l.SaveModuleText("neuf", bad, hash, false); !errors.Is(err, ErrInvalid) || module.Errors(issues) == 0 {
		t.Errorf("texte invalide : %v %v", err, issues)
	}
	if text, _, _ := l.ModuleText("neuf"); text != sk {
		t.Error("fichier modifié malgré le refus")
	}

	// Modification concurrente : refusée
	good := strings.Replace(sk, "ip: 10", "ip: 11", 1)
	if _, _, err := l.SaveModuleText("neuf", good, "autre-empreinte", false); !errors.Is(err, modfiles.ErrConflict) {
		t.Errorf("conflit non détecté : %v", err)
	}
	h2, _, err := l.SaveModuleText("neuf", good, hash, false)
	if err != nil || h2 == hash {
		t.Fatalf("enregistrement : %v", err)
	}
	if text, _, _ := l.ModuleText("neuf"); text != good {
		t.Error("contenu enregistré différent")
	}
}
