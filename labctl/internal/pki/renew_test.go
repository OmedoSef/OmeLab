package pki

import (
	"crypto/x509"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// chainOK vérifie un certificat émis jusqu'à la racine donnée, en passant par son émetteur.
func chainOK(t *testing.T, rootPath, issuerPath, leafPath string) error {
	t.Helper()
	roots, inter := x509.NewCertPool(), x509.NewCertPool()
	roots.AddCert(mustCert(t, rootPath))
	inter.AddCert(mustCert(t, issuerPath))
	_, err := mustCert(t, leafPath).Verify(x509.VerifyOptions{Roots: roots, Intermediates: inter, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageAny}})
	return err
}

// labPKI : racine, intermédiaire, une CA dérivée, un certificat de chaque côté (+ un .p12).
func labPKI(t *testing.T) *PKI {
	t.Helper()
	p := newTestPKI(t)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(p.SubCA("pentest", "pentest.test.lab", "10.66.66.0/24"))
	must(p.Issue(IssueOptions{Name: "web", SubCA: "pentest", IPs: []string{"10.66.66.30"}, DNS: []string{"dvwa.pentest.test.lab"}, Days: 400}))
	must(p.Issue(IssueOptions{Name: "srv", DNS: []string{"www.test.lab"}}))
	must(p.Issue(IssueOptions{Name: "alice", Client: true, PFX: true}))
	return p
}

func TestRenewIntermediate(t *testing.T) {
	p := labPKI(t)
	oldRoot := mustCert(t, p.rootCrt())
	oldInter := mustCert(t, p.intCrt())
	oldSrv := mustCert(t, p.path("certs", "srv", "srv.crt"))
	oldWeb := mustCert(t, p.path("certs", "web", "web.crt"))

	rep, err := p.RenewIntermediate()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(rep.Reissued, []string{"alice", "srv"}) {
		t.Errorf("réémis : %v, attendu alice et srv (pas web, signé par la CA dérivée)", rep.Reissued)
	}
	if !mustCert(t, p.rootCrt()).Equal(oldRoot) {
		t.Error("la racine ne doit pas changer")
	}
	inter := mustCert(t, p.intCrt())
	if inter.Equal(oldInter) || reflect.DeepEqual(inter.SubjectKeyId, oldInter.SubjectKeyId) {
		t.Error("l'intermédiaire doit avoir une nouvelle clé")
	}
	if inter.Subject.String() != oldInter.Subject.String() || !inter.MaxPathLenZero {
		t.Error("même sujet, pathlen 0 conservé")
	}
	srv := mustCert(t, p.path("certs", "srv", "srv.crt"))
	if !reflect.DeepEqual(srv.DNSNames, oldSrv.DNSNames) || reflect.DeepEqual(srv.PublicKey, oldSrv.PublicKey) {
		t.Error("srv : mêmes noms, nouvelle clé attendus")
	}
	if err := chainOK(t, p.rootCrt(), p.intCrt(), p.path("certs", "srv", "srv.crt")); err != nil {
		t.Errorf("srv ne se vérifie pas avec la nouvelle intermédiaire : %v", err)
	}
	if !mustCert(t, p.path("certs", "web", "web.crt")).Equal(oldWeb) {
		t.Error("web (CA dérivée) ne doit pas être touché")
	}
	// Le .p12 d'alice est régénéré avec le nouveau certificat
	b, _ := os.ReadFile(p.path("certs", "alice", "alice.p12"))
	if _, leaf, _, err := decodePFX(b); err != nil || !leaf.Equal(mustCert(t, p.path("certs", "alice", "alice.crt"))) {
		t.Errorf(".p12 d'alice non régénéré : %v", err)
	}
	// Archive : l'ancienne intermédiaire y est intacte
	if a := mustCert(t, filepath.Join(rep.Archive, "intermediate", "intermediate-ca.crt")); !a.Equal(oldInter) {
		t.Error("l'archive doit contenir l'ancienne intermédiaire")
	}
}

func TestRenewSubCA(t *testing.T) {
	p := labPKI(t)
	old := mustCert(t, p.path("sub-ca", "pentest", "ca.crt"))
	oldWeb := mustCert(t, p.path("certs", "web", "web.crt"))
	rep, err := p.RenewSubCA("pentest")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(rep.Reissued, []string{"web"}) {
		t.Errorf("réémis : %v", rep.Reissued)
	}
	ca := mustCert(t, p.path("sub-ca", "pentest", "ca.crt"))
	if reflect.DeepEqual(ca.SubjectKeyId, old.SubjectKeyId) {
		t.Error("nouvelle clé attendue")
	}
	if !reflect.DeepEqual(ca.PermittedDNSDomains, old.PermittedDNSDomains) || ca.PermittedIPRanges[0].String() != "10.66.66.0/24" || !ca.PermittedDNSDomainsCritical {
		t.Errorf("limites perdues : %v %v", ca.PermittedDNSDomains, ca.PermittedIPRanges)
	}
	web := mustCert(t, p.path("certs", "web", "web.crt"))
	if !reflect.DeepEqual(web.DNSNames, oldWeb.DNSNames) || web.IPAddresses[0].String() != "10.66.66.30" {
		t.Errorf("web : noms changés %v %v", web.DNSNames, web.IPAddresses)
	}
	if d := web.NotAfter.Sub(web.NotBefore).Hours() / 24; d < 399 || d > 401 {
		t.Errorf("web : durée de validité %v j, attendu ~400 comme l'ancien", d)
	}
	if err := chainOK(t, p.rootCrt(), p.path("sub-ca", "pentest", "ca.crt"), p.path("certs", "web", "web.crt")); err != nil {
		t.Error(err)
	}
	if _, err := p.RenewSubCA("absente"); err == nil {
		t.Error("CA dérivée inconnue : erreur attendue")
	}
	// Une nouvelle émission avec la CA renouvelée respecte toujours ses limites
	if err := p.Issue(IssueOptions{Name: "evil", SubCA: "pentest", DNS: []string{"google.com"}}); err == nil {
		t.Error("les contraintes de nom doivent survivre au renouvellement")
	}
}

func TestRotateRoot(t *testing.T) {
	p := labPKI(t)
	oldRootPath := filepath.Join(t.TempDir(), "old-root.crt")
	copyFile(p.rootCrt(), oldRootPath, 0o644)
	oldRoot := mustCert(t, oldRootPath)

	rep, err := p.RotateRoot()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(rep.Reissued, []string{"alice", "srv", "web"}) {
		t.Errorf("tous les certificats doivent être réémis : %v", rep.Reissued)
	}
	root := mustCert(t, p.rootCrt())
	if root.Equal(oldRoot) || root.Subject.String() != oldRoot.Subject.String() || root.CheckSignatureFrom(root) != nil {
		t.Error("nouvelle racine auto-signée, même nom attendue")
	}
	cases := [][2]string{
		{p.intCrt(), p.path("certs", "srv", "srv.crt")},
		{p.path("sub-ca", "pentest", "ca.crt"), p.path("certs", "web", "web.crt")},
	}
	for _, c := range cases {
		if err := chainOK(t, p.rootCrt(), c[0], c[1]); err != nil {
			t.Errorf("%s : invalide avec la nouvelle racine : %v", c[1], err)
		}
		if err := chainOK(t, oldRootPath, c[0], c[1]); err == nil {
			t.Errorf("%s : ne doit plus être valide avec l'ancienne racine", c[1])
		}
	}
	// Les limites des CA dérivées sont conservées
	if ca := mustCert(t, p.path("sub-ca", "pentest", "ca.crt")); !reflect.DeepEqual(ca.PermittedDNSDomains, []string{"pentest.test.lab"}) {
		t.Errorf("limites : %v", ca.PermittedDNSDomains)
	}
	// L'archive contient l'ancienne chaîne complète, toujours cohérente
	if err := chainOK(t, filepath.Join(rep.Archive, "root", "root-ca.crt"), filepath.Join(rep.Archive, "intermediate", "intermediate-ca.crt"),
		filepath.Join(rep.Archive, "certs", "srv", "srv.crt")); err != nil {
		t.Errorf("archive incohérente : %v", err)
	}
	if st, _ := os.Stat(filepath.Join(rep.Archive, "root", "root-ca.key")); st == nil || st.Mode().Perm() != 0o600 {
		t.Error("les clés archivées doivent rester en 600")
	}
	if len(p.Archives()) != 1 {
		t.Errorf("archives : %v", p.Archives())
	}
	// Une deuxième rotation n'emporte pas l'archive précédente dans la nouvelle
	rep2, err := p.RotateRoot()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(rep2.Archive, "archive")); err == nil {
		t.Error("une archive ne doit pas contenir les archives précédentes")
	}
}
