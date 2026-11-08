// Package testtool sert aux tests qui ont besoin d'un outil de l'hôte (qemu-img, gpgv, python3…).
package testtool

import (
	"os"
	"os/exec"
	"testing"
)

// Need renvoie le chemin de l'outil name. Absent, le test est sauté ; mais avec LABCTL_TEST_STRICT=1 (posé par
// « build.sh ci ») il échoue : en CI, un test ne disparaît jamais en silence faute d'outil.
func Need(t testing.TB, name string) string {
	t.Helper()
	p, err := exec.LookPath(name)
	if err == nil {
		return p
	}
	if os.Getenv("LABCTL_TEST_STRICT") == "1" {
		t.Fatalf("%s absent : requis pour ce test (LABCTL_TEST_STRICT=1)", name)
	}
	t.Skipf("%s absent", name)
	return ""
}
