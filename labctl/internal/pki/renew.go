package pki

import (
	"bytes"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"software.sslmate.com/src/go-pkcs12"
)

// Renouvellement de la chaîne. Chaque opération :
//   - archive d'abord toute la PKI dans archive/<date>/ (retour arrière : recopier ces fichiers) ;
//   - génère de nouvelles clés (CA et certificats réémis) ;
//   - réémet les certificats concernés à l'identique : mêmes noms, mêmes usages, même durée de validité.

// RenewReport résume un renouvellement.
type RenewReport struct {
	Archive  string   // dossier d'archive de l'ancienne PKI
	Reissued []string // certificats réémis (à redéployer là où ils sont utilisés)
}

// certIssuers associe chaque certificat émis à sa CA : "" pour l'intermédiaire, sinon le nom de la CA dérivée.
func (p *PKI) certIssuers() (map[string]string, error) {
	inter, err := readCert(p.intCrt())
	if err != nil {
		return nil, err
	}
	subs := map[string][]byte{} // nom -> identifiant de clé
	for _, s := range p.SubCANames() {
		if c, err := readCert(p.path("sub-ca", s, "ca.crt")); err == nil {
			subs[s] = c.SubjectKeyId
		}
	}
	out := map[string]string{}
	for _, name := range p.CertNames() {
		c, err := readCert(p.path("certs", name, name+".crt"))
		if err != nil {
			continue
		}
		switch {
		case bytes.Equal(c.AuthorityKeyId, inter.SubjectKeyId):
			out[name] = ""
		default:
			found := false
			for s, ski := range subs {
				if bytes.Equal(c.AuthorityKeyId, ski) {
					out[name], found = s, true
				}
			}
			if !found {
				return nil, fmt.Errorf("certificat %s : émetteur introuvable dans la PKI", name)
			}
		}
	}
	return out, nil
}

// archive copie la PKI (hors archives) dans archive/<date>/.
func (p *PKI) archive() (string, error) {
	dest := p.path("archive", time.Now().Format("20060102-150405"))
	if _, err := os.Stat(dest); err == nil {
		dest += fmt.Sprintf("-%d", time.Now().Nanosecond())
	}
	err := filepath.WalkDir(p.Dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(p.Dir, path)
		if rel == "archive" {
			return filepath.SkipDir
		}
		target := filepath.Join(dest, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		return copyFile(path, target, info.Mode().Perm())
	})
	if err != nil {
		return "", fmt.Errorf("archivage de la PKI : %w", err)
	}
	p.logf("==> ancienne PKI archivée dans %s", dest)
	return dest, nil
}

func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// newCA crée une CA (clé neuve) sur le modèle d'un certificat existant : même sujet, mêmes limites.
func (p *PKI) newCA(model *x509.Certificate, issuer *x509.Certificate, issuerKey crypto.Signer, selfSigned bool, days int) (*x509.Certificate, *rsa.PrivateKey, error) {
	key, err := rsa.GenerateKey(rand.Reader, p.bits(4096))
	if err != nil {
		return nil, nil, err
	}
	tmpl := &x509.Certificate{
		Subject:                     model.Subject,
		BasicConstraintsValid:       true,
		IsCA:                        true,
		MaxPathLen:                  model.MaxPathLen,
		MaxPathLenZero:              model.MaxPathLenZero,
		KeyUsage:                    x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		PermittedDNSDomainsCritical: model.PermittedDNSDomainsCritical,
		PermittedDNSDomains:         model.PermittedDNSDomains,
		ExcludedDNSDomains:          model.ExcludedDNSDomains,
		PermittedIPRanges:           model.PermittedIPRanges,
		ExcludedIPRanges:            model.ExcludedIPRanges,
	}
	parent, signer := issuer, issuerKey
	if selfSigned {
		parent, signer = tmpl, key
	}
	der, err := p.sign(tmpl, parent, &key.PublicKey, signer, days)
	if err != nil {
		return nil, nil, err
	}
	c, err := x509.ParseCertificate(der)
	return c, key, err
}

// reissue réémet un certificat sur le modèle de l'ancien, signé par ca.
func (p *PKI) reissue(name string, ca *x509.Certificate, caKey crypto.Signer) error {
	base := p.path("certs", name, name)
	old, err := readCert(base + ".crt")
	if err != nil {
		return err
	}
	days := int(old.NotAfter.Sub(old.NotBefore).Hours()/24 + 0.5)
	if days < 1 || days > DefaultDays {
		days = DefaultDays
	}
	key, err := rsa.GenerateKey(rand.Reader, p.bits(2048))
	if err != nil {
		return err
	}
	tmpl := &x509.Certificate{
		Subject:               old.Subject,
		BasicConstraintsValid: true,
		KeyUsage:              old.KeyUsage,
		ExtKeyUsage:           old.ExtKeyUsage,
		DNSNames:              old.DNSNames,
		IPAddresses:           old.IPAddresses,
	}
	der, err := p.sign(tmpl, ca, &key.PublicKey, caKey, days)
	if err != nil {
		return err
	}
	leaf, _ := x509.ParseCertificate(der)
	if err := p.verify(leaf, ca); err != nil {
		return fmt.Errorf("certificat %s réémis refusé à la vérification : %w", name, err)
	}
	if err := writeKey(base+".key", key); err != nil {
		return err
	}
	if err := writeCerts(base+".crt", der); err != nil {
		return err
	}
	if err := writeCerts(base+".fullchain.crt", der, ca.Raw); err != nil {
		return err
	}
	if _, err := os.Stat(base + ".p12"); err == nil {
		root, err := readCert(p.rootCrt())
		if err != nil {
			return err
		}
		pfx, err := pkcs12.Modern.Encode(key, leaf, []*x509.Certificate{ca, root}, "")
		if err != nil {
			return err
		}
		if err := os.WriteFile(base+".p12", pfx, 0o600); err != nil {
			return err
		}
	}
	p.logf("ok  certificat %s réémis (%s)", name, strings.Join(SANs(leaf), ","))
	return nil
}

// reissueFrom réémet tous les certificats dont l'émetteur est issuerName ("" = intermédiaire).
func (p *PKI) reissueFrom(issuers map[string]string, issuerName string, ca *x509.Certificate, caKey crypto.Signer, rep *RenewReport) error {
	for _, name := range p.CertNames() {
		if iss, ok := issuers[name]; ok && iss == issuerName {
			if err := p.reissue(name, ca, caKey); err != nil {
				return err
			}
			rep.Reissued = append(rep.Reissued, name)
		}
	}
	return nil
}

// RenewIntermediate remplace la CA intermédiaire (nouvelle clé, même racine) et réémet ses certificats.
// Les VMs n'ont rien à faire : elles font confiance à la racine, inchangée.
func (p *PKI) RenewIntermediate() (*RenewReport, error) {
	if !p.Initialized() {
		return nil, ErrNotInitialized
	}
	issuers, err := p.certIssuers()
	if err != nil {
		return nil, err
	}
	old, err := readCert(p.intCrt())
	if err != nil {
		return nil, err
	}
	root, rootKey, err := loadCA(p.rootCrt(), p.rootKey())
	if err != nil {
		return nil, err
	}
	rep := &RenewReport{}
	if rep.Archive, err = p.archive(); err != nil {
		return nil, err
	}
	p.logf("==> nouvelle CA intermédiaire")
	inter, key, err := p.newCA(old, root, rootKey, false, caDays)
	if err != nil {
		return nil, err
	}
	if err := writeKey(p.intKey(), key); err != nil {
		return nil, err
	}
	if err := writeCerts(p.intCrt(), inter.Raw); err != nil {
		return nil, err
	}
	if err := writeCerts(p.path("intermediate", "ca-chain.crt"), inter.Raw, root.Raw); err != nil {
		return nil, err
	}
	return rep, p.reissueFrom(issuers, "", inter, key, rep)
}

// RenewSubCA remplace une CA dérivée (nouvelle clé, mêmes limites) et réémet ses certificats.
func (p *PKI) RenewSubCA(name string) (*RenewReport, error) {
	if !p.Initialized() {
		return nil, ErrNotInitialized
	}
	dir := p.path("sub-ca", name)
	old, err := readCert(filepath.Join(dir, "ca.crt"))
	if err != nil {
		return nil, fmt.Errorf("CA dérivée inconnue : %s", name)
	}
	issuers, err := p.certIssuers()
	if err != nil {
		return nil, err
	}
	root, rootKey, err := loadCA(p.rootCrt(), p.rootKey())
	if err != nil {
		return nil, err
	}
	rep := &RenewReport{}
	if rep.Archive, err = p.archive(); err != nil {
		return nil, err
	}
	p.logf("==> nouvelle CA dérivée %s", name)
	ca, key, err := p.newCA(old, root, rootKey, false, caDays)
	if err != nil {
		return nil, err
	}
	if err := p.writeSubCA(name, ca, key, root); err != nil {
		return nil, err
	}
	return rep, p.reissueFrom(issuers, name, ca, key, rep)
}

func (p *PKI) writeSubCA(name string, ca *x509.Certificate, key *rsa.PrivateKey, root *x509.Certificate) error {
	dir := p.path("sub-ca", name)
	if err := writeKey(filepath.Join(dir, "ca.key"), key); err != nil {
		return err
	}
	if err := writeCerts(filepath.Join(dir, "ca.crt"), ca.Raw); err != nil {
		return err
	}
	return writeCerts(filepath.Join(dir, "ca-chain.crt"), ca.Raw, root.Raw)
}

// RotateRoot régénère toute la chaîne : nouvelle racine, nouvelle intermédiaire, nouvelles CA dérivées
// (mêmes limites), tous les certificats réémis. Les VMs ne font plus confiance qu'à l'ancienne racine :
// il faut leur installer la nouvelle (TrustVM) et redéployer les certificats réémis.
func (p *PKI) RotateRoot() (*RenewReport, error) {
	if !p.Initialized() {
		return nil, ErrNotInitialized
	}
	issuers, err := p.certIssuers()
	if err != nil {
		return nil, err
	}
	oldRoot, err := readCert(p.rootCrt())
	if err != nil {
		return nil, err
	}
	oldInter, err := readCert(p.intCrt())
	if err != nil {
		return nil, err
	}
	oldSubs := map[string]*x509.Certificate{}
	for _, s := range p.SubCANames() {
		c, err := readCert(p.path("sub-ca", s, "ca.crt"))
		if err != nil {
			return nil, err
		}
		oldSubs[s] = c
	}
	rep := &RenewReport{}
	if rep.Archive, err = p.archive(); err != nil {
		return nil, err
	}

	p.logf("==> nouvelle CA racine")
	root, rootKey, err := p.newCA(oldRoot, nil, nil, true, rootDays)
	if err != nil {
		return nil, err
	}
	p.logf("==> nouvelle CA intermédiaire")
	inter, interKey, err := p.newCA(oldInter, root, rootKey, false, caDays)
	if err != nil {
		return nil, err
	}
	if err := writeKey(p.rootKey(), rootKey); err != nil {
		return nil, err
	}
	if err := writeCerts(p.rootCrt(), root.Raw); err != nil {
		return nil, err
	}
	if err := writeKey(p.intKey(), interKey); err != nil {
		return nil, err
	}
	if err := writeCerts(p.intCrt(), inter.Raw); err != nil {
		return nil, err
	}
	if err := writeCerts(p.path("intermediate", "ca-chain.crt"), inter.Raw, root.Raw); err != nil {
		return nil, err
	}
	if err := p.reissueFrom(issuers, "", inter, interKey, rep); err != nil {
		return nil, err
	}
	for _, s := range p.SubCANames() {
		p.logf("==> nouvelle CA dérivée %s", s)
		ca, key, err := p.newCA(oldSubs[s], root, rootKey, false, caDays)
		if err != nil {
			return nil, err
		}
		if err := p.writeSubCA(s, ca, key, root); err != nil {
			return nil, err
		}
		if err := p.reissueFrom(issuers, s, ca, key, rep); err != nil {
			return nil, err
		}
	}
	return rep, nil
}

// Archives liste les archives de la PKI (plus récente en dernier).
func (p *PKI) Archives() []string { return dirNames(p.path("archive")) }
