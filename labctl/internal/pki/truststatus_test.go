package pki

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"math/big"
	"testing"
	"time"
)

func makeCertPEM(t *testing.T, cn string) ([]byte, []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: cn},
		NotBefore:    time.Now(), NotAfter: time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), der
}

// wrap renvoie un PEM dont le corps base64 est découpé en lignes de `n` caractères (au lieu de 64) : un magasin
// système peut ré-habiller les certificats ; la détection ne doit pas en dépendre.
func wrap(der []byte, n int) []byte {
	b64 := base64.StdEncoding.EncodeToString(der)
	var buf bytes.Buffer
	buf.WriteString("-----BEGIN CERTIFICATE-----\n")
	for i := 0; i < len(b64); i += n {
		end := i + n
		if end > len(b64) {
			end = len(b64)
		}
		buf.WriteString(b64[i:end])
		buf.WriteByte('\n')
	}
	buf.WriteString("-----END CERTIFICATE-----\n")
	return buf.Bytes()
}

func TestCertInBundleDetection(t *testing.T) {
	aPEM, aDER := makeCertPEM(t, "CA A")
	bPEM, _ := makeCertPEM(t, "CA B")
	cPEM, _ := makeCertPEM(t, "CA C") // absente du bundle

	// Bundle façon /etc/ssl/certs/ca-certificates.crt : du texte, A (ré-habillé en 48), puis B.
	var bundle bytes.Buffer
	bundle.WriteString("Mozilla CA bundle\n")
	bundle.Write(wrap(aDER, 48))
	bundle.Write(bPEM)
	flat := stripSpace(bundle.Bytes())

	if !bytes.Contains(flat, pemBase64(aPEM)) {
		t.Error("A devrait être détectée (malgré un habillage de lignes différent)")
	}
	if !bytes.Contains(flat, pemBase64(bPEM)) {
		t.Error("B devrait être détectée")
	}
	if bytes.Contains(flat, pemBase64(cPEM)) {
		t.Error("C ne devrait PAS être détectée")
	}
	if pemBase64([]byte("pas du PEM")) != nil {
		t.Error("pemBase64 d'un non-PEM devrait être nil")
	}
}
