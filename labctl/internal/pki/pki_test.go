package pki

import (
	"bytes"
	"crypto/x509"
	"encoding/pem"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"software.sslmate.com/src/go-pkcs12"
)

// newTestPKI crée une PKI initialisée dans un dossier temporaire (clés courtes pour aller vite).
func newTestPKI(t *testing.T) *PKI {
	t.Helper()
	p := &PKI{Dir: filepath.Join(t.TempDir(), "pki"), Org: "Test Labs", Domain: "test.lab", KeyBits: 1024}
	if err := p.Init(); err != nil {
		t.Fatal(err)
	}
	return p
}

func mustCert(t *testing.T, path string) *x509.Certificate {
	t.Helper()
	c, err := readCert(path)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func countPEM(t *testing.T, path string) int {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return bytes.Count(b, []byte("BEGIN CERTIFICATE"))
}

func TestInit(t *testing.T) {
	p := newTestPKI(t)
	root := mustCert(t, p.rootCrt())
	inter := mustCert(t, p.intCrt())
	if !root.IsCA || root.Subject.CommonName != "Test Labs Root CA" || root.CheckSignatureFrom(root) != nil {
		t.Errorf("racine invalide : %+v", root.Subject)
	}
	if !inter.IsCA || !inter.MaxPathLenZero || inter.CheckSignatureFrom(root) != nil {
		t.Error("intermédiaire : doit être une CA pathlen 0 signée par la racine")
	}
	if !bytes.Equal(inter.AuthorityKeyId, root.SubjectKeyId) {
		t.Error("identifiant de clé de l'autorité incorrect")
	}
	if n := countPEM(t, p.path("intermediate", "ca-chain.crt")); n != 2 {
		t.Errorf("ca-chain.crt : %d certificats, attendu 2", n)
	}
	for _, k := range []string{p.rootKey(), p.intKey()} {
		if st, _ := os.Stat(k); st.Mode().Perm() != 0o600 {
			t.Errorf("%s : droits %v, attendu 600", k, st.Mode().Perm())
		}
	}
	if st, _ := os.Stat(p.Dir); st.Mode().Perm() != 0o700 {
		t.Errorf("dossier PKI : droits %v, attendu 700", st.Mode().Perm())
	}
	if err := p.Init(); err == nil {
		t.Error("un second init doit échouer (la racine ne doit jamais être écrasée)")
	}
}

func TestSubCA(t *testing.T) {
	p := newTestPKI(t)
	if err := p.SubCA("pentest", "pentest.test.lab", "10.66.66.0/24"); err != nil {
		t.Fatal(err)
	}
	c := mustCert(t, p.path("sub-ca", "pentest", "ca.crt"))
	if !c.PermittedDNSDomainsCritical || !reflect.DeepEqual(c.PermittedDNSDomains, []string{"pentest.test.lab"}) {
		t.Errorf("contrainte DNS : %v (critique : %v)", c.PermittedDNSDomains, c.PermittedDNSDomainsCritical)
	}
	if len(c.PermittedIPRanges) != 1 || c.PermittedIPRanges[0].String() != "10.66.66.0/24" {
		t.Errorf("contrainte IP : %v", c.PermittedIPRanges)
	}
	if c.CheckSignatureFrom(mustCert(t, p.rootCrt())) != nil {
		t.Error("une CA dérivée est signée par la racine")
	}
	// Idempotent : la CA existante est conservée telle quelle
	if err := p.SubCA("pentest", "autre.test.lab", ""); err != nil {
		t.Fatal(err)
	}
	if again := mustCert(t, p.path("sub-ca", "pentest", "ca.crt")); !again.Equal(c) {
		t.Error("une CA dérivée existante ne doit pas être recréée")
	}
	// Sans sous-réseau : aucune IP autorisée
	if err := p.SubCA("noip", "noip.test.lab", ""); err != nil {
		t.Fatal(err)
	}
	if c := mustCert(t, p.path("sub-ca", "noip", "ca.crt")); len(c.ExcludedIPRanges) != 1 || c.ExcludedIPRanges[0].String() != "0.0.0.0/0" {
		t.Errorf("sans -s, toutes les IP doivent être exclues : %v", c.ExcludedIPRanges)
	}
	for _, bad := range [][3]string{{"bad name", "x.lab", ""}, {"ok", "", ""}, {"ok", "x.lab", "pas-un-cidr"}} {
		if err := p.SubCA(bad[0], bad[1], bad[2]); err == nil {
			t.Errorf("SubCA(%q, %q, %q) doit échouer", bad[0], bad[1], bad[2])
		}
	}
}

func TestIssueIntermediate(t *testing.T) {
	p := newTestPKI(t)
	if err := p.Issue(IssueOptions{Name: "web", DNS: []string{"www.test.lab", "*.apps.test.lab"}, IPs: []string{"10.1.1.1"}}); err != nil {
		t.Fatal(err)
	}
	c := mustCert(t, p.path("certs", "web", "web.crt"))
	want := []string{"web", "web.test.lab", "www.test.lab", "*.apps.test.lab"}
	if !reflect.DeepEqual(c.DNSNames, want) || len(c.IPAddresses) != 1 || c.IPAddresses[0].String() != "10.1.1.1" {
		t.Errorf("SAN = %v %v, attendu %v + 10.1.1.1", c.DNSNames, c.IPAddresses, want)
	}
	if !reflect.DeepEqual(c.ExtKeyUsage, []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth}) || c.IsCA {
		t.Errorf("usages : %v, CA : %v", c.ExtKeyUsage, c.IsCA)
	}
	if n := countPEM(t, p.path("certs", "web", "web.fullchain.crt")); n != 2 {
		t.Errorf("fullchain : %d certificats, attendu 2 (certificat + intermédiaire)", n)
	}
	if err := p.Issue(IssueOptions{Name: "web"}); err == nil {
		t.Error("réémettre un certificat existant doit échouer")
	}
	// Nom complet : pas de suffixe ajouté
	if err := p.Issue(IssueOptions{Name: "db1.data.test.lab"}); err != nil {
		t.Fatal(err)
	}
	if c := mustCert(t, p.path("certs", "db1.data.test.lab", "db1.data.test.lab.crt")); !reflect.DeepEqual(c.DNSNames, []string{"db1.data.test.lab"}) {
		t.Errorf("FQDN : %v", c.DNSNames)
	}
	// Certificat client
	if err := p.Issue(IssueOptions{Name: "alice", Client: true, Days: 30}); err != nil {
		t.Fatal(err)
	}
	if c := mustCert(t, p.path("certs", "alice", "alice.crt")); !reflect.DeepEqual(c.ExtKeyUsage, []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}) {
		t.Errorf("client : %v", c.ExtKeyUsage)
	}
	for _, bad := range []IssueOptions{{Name: "*.x"}, {Name: "bad name"}, {Name: "ok", IPs: []string{"999.1.1.1"}}, {Name: "ok2", SubCA: "absente"}} {
		if err := p.Issue(bad); err == nil {
			t.Errorf("Issue(%+v) doit échouer", bad)
		}
	}
}

func TestIssueSubCAConstraints(t *testing.T) {
	p := newTestPKI(t)
	if err := p.SubCA("pentest", "pentest.test.lab", "10.66.66.0/24"); err != nil {
		t.Fatal(err)
	}
	if err := p.Issue(IssueOptions{Name: "web", SubCA: "pentest", IPs: []string{"10.66.66.30"}, DNS: []string{"dvwa.pentest.test.lab"}}); err != nil {
		t.Fatal(err)
	}
	c := mustCert(t, p.path("certs", "web", "web.crt"))
	if !reflect.DeepEqual(c.DNSNames, []string{"web.pentest.test.lab", "dvwa.pentest.test.lab"}) {
		t.Errorf("sous une CA dérivée : pas de nom court, domaine de la CA ; obtenu %v", c.DNSNames)
	}
	if c.Issuer.CommonName != "Test Labs pentest CA" {
		t.Errorf("émetteur : %s", c.Issuer.CommonName)
	}
	// Hors des limites : refusé, et aucun fichier laissé
	for _, bad := range []IssueOptions{
		{Name: "evil", SubCA: "pentest", DNS: []string{"google.com"}},
		{Name: "evil2", SubCA: "pentest", IPs: []string{"8.8.8.8"}},
		{Name: "evil3", SubCA: "pentest", Domain: "omedo.lab"},
	} {
		if err := p.Issue(bad); err == nil {
			t.Errorf("Issue(%+v) doit être refusé par la contrainte de nom", bad)
		}
		if _, err := os.Stat(p.path("certs", bad.Name)); err == nil {
			t.Errorf("%s refusé mais son dossier existe", bad.Name)
		}
	}
}

func TestIssuePFX(t *testing.T) {
	p := newTestPKI(t)
	if err := p.SubCA("pentest", "pentest.test.lab", ""); err != nil {
		t.Fatal(err)
	}
	if err := p.Issue(IssueOptions{Name: "bob", SubCA: "pentest", Client: true, PFX: true}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(p.path("certs", "bob", "bob.p12"))
	if err != nil {
		t.Fatal(err)
	}
	key, leaf, chain, err := pkcs12.DecodeChain(b, "")
	if err != nil {
		t.Fatal(err)
	}
	if key == nil || leaf.Subject.CommonName != "bob" || len(chain) != 2 {
		t.Errorf(".p12 : clé %v, certificat %s, chaîne de %d (attendu 2 : CA dérivée + racine)", key != nil, leaf.Subject.CommonName, len(chain))
	}
}

// TestOpenSSLInterop vérifie la compatibilité avec openssl dans les deux sens (ignoré sans openssl).
func TestOpenSSLInterop(t *testing.T) {
	if _, err := exec.LookPath("openssl"); err != nil {
		t.Skip("openssl absent")
	}
	openssl := func(args ...string) string {
		t.Helper()
		out, err := exec.Command("openssl", args...).CombinedOutput()
		if err != nil {
			t.Fatalf("openssl %s : %v\n%s", strings.Join(args, " "), err, out)
		}
		return string(out)
	}

	// 1. openssl valide une chaîne produite par Go, contraintes de nom comprises
	p := newTestPKI(t)
	if err := p.SubCA("pentest", "pentest.test.lab", "10.66.66.0/24"); err != nil {
		t.Fatal(err)
	}
	if err := p.Issue(IssueOptions{Name: "web", SubCA: "pentest", IPs: []string{"10.66.66.30"}}); err != nil {
		t.Fatal(err)
	}
	out := openssl("verify", "-CAfile", p.rootCrt(), "-untrusted", p.path("sub-ca", "pentest", "ca.crt"), p.path("certs", "web", "web.crt"))
	if !strings.Contains(out, ": OK") {
		t.Errorf("openssl verify (CA dérivée) : %s", out)
	}
	if err := p.Issue(IssueOptions{Name: "srv"}); err != nil {
		t.Fatal(err)
	}
	out = openssl("verify", "-CAfile", p.rootCrt(), "-untrusted", p.intCrt(), p.path("certs", "srv", "srv.crt"))
	if !strings.Contains(out, ": OK") {
		t.Errorf("openssl verify (intermédiaire) : %s", out)
	}
	if !strings.Contains(openssl("x509", "-in", p.path("sub-ca", "pentest", "ca.crt"), "-noout", "-text"), "Name Constraints: critical") {
		t.Error("openssl doit voir une contrainte de nom critique")
	}

	// 2. Go signe avec une racine créée par openssl (cas de la PKI existante, clé PKCS#8)
	q := &PKI{Dir: filepath.Join(t.TempDir(), "pki"), Org: "Test Labs", Domain: "test.lab", KeyBits: 1024}
	os.MkdirAll(filepath.Join(q.Dir, "root"), 0o700)
	openssl("req", "-x509", "-newkey", "rsa:2048", "-nodes", "-days", "30", "-subj", "/O=Test Labs/CN=Test Labs Root CA",
		"-addext", "basicConstraints=critical,CA:TRUE", "-addext", "keyUsage=critical,keyCertSign,cRLSign",
		"-keyout", q.rootKey(), "-out", q.rootCrt())
	if b, _ := os.ReadFile(q.rootKey()); !bytes.Contains(b, []byte("BEGIN PRIVATE KEY")) {
		blk, _ := pem.Decode(b)
		t.Fatalf("clé openssl inattendue : %v", blk.Type)
	}
	if err := q.SubCA("legacy", "legacy.test.lab", ""); err != nil {
		t.Fatalf("signature avec une racine openssl : %v", err)
	}
	out = openssl("verify", "-CAfile", q.rootCrt(), q.path("sub-ca", "legacy", "ca.crt"))
	if !strings.Contains(out, ": OK") {
		t.Errorf("openssl verify (CA dérivée signée par Go avec une racine openssl) : %s", out)
	}
}

func decodePFX(b []byte) (any, *x509.Certificate, []*x509.Certificate, error) {
	return pkcs12.DecodeChain(b, "")
}
