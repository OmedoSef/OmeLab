package cli

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"labs/labctl/internal/gui"
	"labs/labctl/internal/module"
)

// Un module ne doit pas pouvoir porter le nom d'une commande de labctl : il deviendrait une sous-commande en
// collision avec elle (labctl <nom>, labctl module <nom>). La liste des noms réservés suit donc les commandes réelles.
func TestReservedNamesCoverCommands(t *testing.T) {
	root := t.TempDir() // lab vide : toutes les commandes intégrées, aucune de module
	if err := os.WriteFile(filepath.Join(root, "config.env"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "modules"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LABS_ROOT", root)
	cmd := newRoot()
	var names []string
	for _, c := range cmd.Commands() {
		names = append(names, c.Name())
		if c.Name() == "module" {
			for _, sub := range c.Commands() {
				names = append(names, sub.Name())
			}
		}
	}
	for _, n := range names {
		if !slices.Contains(module.ReservedNames, n) {
			t.Errorf("commande %q absente de module.ReservedNames", n)
		}
	}
	// Chaque commande que la console de la GUI autorise existe vraiment
	for _, c := range gui.ConsoleCommands() {
		if !slices.Contains(names, c) {
			t.Errorf("la GUI autorise %q, qui n'est pas une commande de labctl", c)
		}
	}
}
