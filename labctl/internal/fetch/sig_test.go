package fetch

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"labs/labctl/internal/testtool"
)

// Les clés embarquées se décodent toutes.
func TestEmbeddedKeysDearmor(t *testing.T) {
	for _, k := range []string{"ubuntu", "rocky", "alma"} {
		a, err := signingKeys.ReadFile("keys/" + k + ".asc")
		if err != nil {
			t.Fatal(err)
		}
		b, err := dearmor(a)
		if err != nil || len(b) < 100 || b[0]&0x80 == 0 { // paquet OpenPGP : bit de poids fort à 1
			t.Errorf("%s : clé mal décodée (%d octets, %v)", k, len(b), err)
		}
	}
	if _, err := dearmor([]byte("pas une clé")); err == nil {
		t.Error("un texte quelconque ne doit pas passer pour une clé")
	}
}

// Vraie signature publiée par Rocky Linux (testdata/gpg) : acceptée avec sa clé, refusée si le fichier
// d'empreintes est altéré ou si la clé est celle d'un autre éditeur.
func TestVerifySumsRealSignature(t *testing.T) {
	testtool.Need(t, "gpgv")
	sums, err := os.ReadFile(filepath.Join("testdata", "CHECKSUM"))
	if err != nil {
		t.Fatal(err)
	}
	sig, err := os.ReadFile(filepath.Join("testdata", "CHECKSUM.asc"))
	if err != nil {
		t.Fatal(err)
	}
	if err := verifySums("rocky", sums, sig); err != nil {
		t.Fatalf("signature authentique refusée : %v", err)
	}
	altered := bytes.Replace(sums, []byte("SHA256"), []byte("SHA257"), 1)
	if err := verifySums("rocky", altered, sig); err == nil {
		t.Error("fichier d'empreintes altéré accepté")
	}
	if err := verifySums("alma", sums, sig); err == nil {
		t.Error("signature acceptée avec la clé d'un autre éditeur")
	}
	if err := verifySums("inconnue", sums, sig); err == nil {
		t.Error("clé inconnue acceptée")
	}
}
