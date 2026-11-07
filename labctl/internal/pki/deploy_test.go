package pki

import (
	"strings"
	"testing"
)

// Le nom du certificat à déployer est vérifié avant tout accès aux fichiers ou SSH : pas de sortie de certs/, rien
// d'interprétable par le shell.
func TestDeployRejectsUnsafeName(t *testing.T) {
	p := &PKI{Dir: t.TempDir()}
	for _, name := range []string{"../root/root-ca", "a/b", "x;id", "$(id)", "a..b", ""} {
		err := p.Deploy(Remote{}, "vm", name, "")
		if err == nil || !strings.Contains(err.Error(), "nom de certificat invalide") {
			t.Errorf("nom %q : erreur attendue, obtenu %v", name, err)
		}
	}
}
