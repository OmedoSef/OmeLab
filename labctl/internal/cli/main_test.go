package cli

import (
	"os"
	"testing"
)

// TestMain isole les tests de la configuration de l'utilisateur : le lab par défaut (labctl init, labctl use) est
// écrit dans un XDG_CONFIG_HOME temporaire, jamais dans ~/.config/labctl.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "labctl-config-")
	if err != nil {
		panic(err)
	}
	os.Setenv("XDG_CONFIG_HOME", dir)
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}
