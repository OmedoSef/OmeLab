// Package pki gère la PKI des labs : CA racine -> CA intermédiaire -> certificats, et CA dérivées
// (signées par la racine, limitées à un domaine et un sous-réseau par nameConstraints).
//
// Les fichiers sont compatibles avec ceux de l'ancien script pki.sh (openssl) :
//
//	root/root-ca.{key,crt}
//	intermediate/intermediate-ca.{key,crt}, intermediate/ca-chain.crt (intermédiaire + racine)
//	sub-ca/<nom>/{ca.key,ca.crt,ca-chain.crt,domain}
//	certs/<nom>/{<nom>.key,<nom>.crt,<nom>.fullchain.crt[,<nom>.p12]}
//
// Clés RSA en PKCS#8 PEM, non chiffrées (usage lab) : le dossier ne doit pas être versionné.
package pki

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"software.sslmate.com/src/go-pkcs12"
)

const (
	rootDays = 7300 // 20 ans
	caDays   = 3650 // 10 ans (intermédiaire, CA dérivées)
	// DefaultDays est la validité par défaut d'un certificat : 825 jours, maximum accepté par Apple
	DefaultDays = 825
)

// PKI est une PKI sur disque.
type PKI struct {
	Dir    string    // dossier racine (pki/)
	Org    string    // organisation (O=) des certificats
	Domain string    // domaine racine des labs (ajouté aux noms courts)
	Out    io.Writer // messages de progression
	// KeyBits permet aux tests d'utiliser des clés plus courtes (0 = tailles normales)
	KeyBits int
}

var (
	ErrNotInitialized = errors.New("PKI non initialisée : labctl pki init")
	reCAName          = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9-]*$`)
	reCertName        = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9.-]*$`)
)

func (p *PKI) path(parts ...string) string {
	return filepath.Join(append([]string{p.Dir}, parts...)...)
}

func (p *PKI) rootCrt() string { return p.path("root", "root-ca.crt") }
func (p *PKI) rootKey() string { return p.path("root", "root-ca.key") }
func (p *PKI) intCrt() string  { return p.path("intermediate", "intermediate-ca.crt") }
func (p *PKI) intKey() string  { return p.path("intermediate", "intermediate-ca.key") }

// RootCert renvoie le chemin du certificat racine (à distribuer aux VMs et aux navigateurs).
func (p *PKI) RootCert() string { return p.rootCrt() }

// Initialized indique si la CA racine existe.
func (p *PKI) Initialized() bool {
	_, err := os.Stat(p.rootKey())
	return err == nil
}

func (p *PKI) logf(format string, a ...any) {
	if p.Out != nil {
		fmt.Fprintf(p.Out, format+"\n", a...)
	}
}

func (p *PKI) bits(normal int) int {
	if p.KeyBits > 0 {
		return p.KeyBits
	}
	return normal
}

// ---------------------------------------------------------------- init

// Init crée la CA racine (20 ans) et la CA intermédiaire (10 ans, pathlen 0).
func (p *PKI) Init() error {
	if p.Initialized() {
		return fmt.Errorf("la CA racine existe déjà : %s", p.rootCrt())
	}
	if err := os.MkdirAll(p.Dir, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(p.Dir, 0o700); err != nil {
		return err
	}
	for _, d := range []string{"root", "intermediate", "certs", "sub-ca"} {
		if err := os.MkdirAll(p.path(d), 0o755); err != nil {
			return err
		}
	}

	p.logf("==> CA racine")
	rootKey, err := rsa.GenerateKey(rand.Reader, p.bits(4096))
	if err != nil {
		return err
	}
	root := &x509.Certificate{
		Subject:               pkix.Name{Organization: []string{p.Org}, CommonName: p.Org + " Root CA"},
		BasicConstraintsValid: true, IsCA: true,
		KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}
	rootDER, err := p.sign(root, root, &rootKey.PublicKey, rootKey, rootDays)
	if err != nil {
		return err
	}
	rootCert, _ := x509.ParseCertificate(rootDER)

	p.logf("==> CA intermédiaire")
	intKey, err := rsa.GenerateKey(rand.Reader, p.bits(4096))
	if err != nil {
		return err
	}
	inter := &x509.Certificate{
		Subject:               pkix.Name{Organization: []string{p.Org}, CommonName: p.Org + " Intermediate CA"},
		BasicConstraintsValid: true, IsCA: true, MaxPathLenZero: true,
		KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}
	intDER, err := p.sign(inter, rootCert, &intKey.PublicKey, rootKey, caDays)
	if err != nil {
		return err
	}

	if err := writeKey(p.rootKey(), rootKey); err != nil {
		return err
	}
	if err := writeCerts(p.rootCrt(), rootDER); err != nil {
		return err
	}
	if err := writeKey(p.intKey(), intKey); err != nil {
		return err
	}
	if err := writeCerts(p.intCrt(), intDER); err != nil {
		return err
	}
	if err := writeCerts(p.path("intermediate", "ca-chain.crt"), intDER, rootDER); err != nil {
		return err
	}
	p.logf("ok  CA racine        : %s", p.rootCrt())
	p.logf("ok  CA intermédiaire : %s", p.intCrt())
	return nil
}

// ---------------------------------------------------------------- sub-ca

// SubCA crée une CA dérivée signée par la racine, limitée à domain (et ses sous-domaines) et, pour les
// adresses IP, au sous-réseau cidr (aucune IP autorisée si cidr est vide). Idempotent : ne refait rien
// si la CA existe déjà.
func (p *PKI) SubCA(name, domain, cidr string) error {
	if !p.Initialized() {
		return ErrNotInitialized
	}
	if !reCAName.MatchString(name) {
		return fmt.Errorf("nom invalide : %s", name)
	}
	if domain == "" {
		return errors.New("domaine obligatoire (-D)")
	}
	dir := p.path("sub-ca", name)
	if _, err := os.Stat(filepath.Join(dir, "ca.crt")); err == nil {
		b, _ := os.ReadFile(filepath.Join(dir, "domain"))
		p.logf("ok  CA dérivée %s déjà présente (%s)", name, strings.TrimSpace(string(b)))
		return nil
	}
	tmpl := &x509.Certificate{
		Subject: pkix.Name{
			Organization: []string{p.Org}, OrganizationalUnit: []string{name},
			CommonName: fmt.Sprintf("%s %s CA", p.Org, name),
		},
		BasicConstraintsValid: true, IsCA: true, MaxPathLenZero: true,
		KeyUsage:                    x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		PermittedDNSDomainsCritical: true,
		PermittedDNSDomains:         []string{domain},
	}
	limits := "permitted;DNS:" + domain
	if cidr != "" {
		_, n, err := net.ParseCIDR(cidr)
		if err != nil || n.IP.To4() == nil {
			return fmt.Errorf("CIDR IPv4 invalide : %s", cidr)
		}
		tmpl.PermittedIPRanges = []*net.IPNet{n}
		limits += ",permitted;IP:" + n.String()
	} else {
		tmpl.ExcludedIPRanges = []*net.IPNet{{IP: net.IPv4zero.To4(), Mask: net.CIDRMask(0, 32)}}
		limits += ",excluded;IP:0.0.0.0/0"
	}

	root, rootKey, err := loadCA(p.rootCrt(), p.rootKey())
	if err != nil {
		return err
	}
	key, err := rsa.GenerateKey(rand.Reader, p.bits(4096))
	if err != nil {
		return err
	}
	der, err := p.sign(tmpl, root, &key.PublicKey, rootKey, caDays)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := writeKey(filepath.Join(dir, "ca.key"), key); err != nil {
		return err
	}
	if err := writeCerts(filepath.Join(dir, "ca.crt"), der); err != nil {
		return err
	}
	if err := writeCerts(filepath.Join(dir, "ca-chain.crt"), der, root.Raw); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "domain"), []byte(domain+"\n"), 0o644); err != nil {
		return err
	}
	p.logf("ok  CA dérivée %s (%s) -> %s", name, limits, dir)
	return nil
}

// ---------------------------------------------------------------- issue

// IssueOptions décrit un certificat à émettre.
type IssueOptions struct {
	Name   string   // nom court ou FQDN ; sert aussi de nom de dossier
	SubCA  string   // CA dérivée émettrice ("" = intermédiaire)
	Domain string   // domaine ajouté au nom court ("" = celui de la CA dérivée, ou le domaine racine)
	DNS    []string // noms DNS supplémentaires (wildcards acceptés)
	IPs    []string // adresses IP
	Client bool     // certificat client seulement (sinon serveur + client)
	Days   int      // validité (0 = DefaultDays)
	PFX    bool     // génère aussi <nom>.p12 (mot de passe vide)
}

// Issue émet un certificat. Il est vérifié contre la chaîne (y compris les contraintes de nom de la
// CA dérivée) avant d'être conservé : un certificat refusé ne laisse aucun fichier.
func (p *PKI) Issue(o IssueOptions) error {
	if _, err := os.Stat(p.intKey()); err != nil {
		return ErrNotInitialized
	}
	if !reCertName.MatchString(o.Name) {
		return fmt.Errorf("nom invalide : %s (pour un wildcard : labctl pki issue apps -d '*.apps.%s')", o.Name, p.Domain)
	}
	dir := p.path("certs", o.Name)
	if _, err := os.Stat(filepath.Join(dir, o.Name+".crt")); err == nil {
		return fmt.Errorf("certificat déjà émis : %s (supprimer le dossier pour le réémettre)", filepath.Join(dir, o.Name+".crt"))
	}

	// CA émettrice
	caCrt, caKey, bare, domain := p.intCrt(), p.intKey(), true, p.Domain
	if o.SubCA != "" {
		sub := p.path("sub-ca", o.SubCA)
		if _, err := os.Stat(filepath.Join(sub, "ca.crt")); err != nil {
			return fmt.Errorf("CA dérivée inconnue : %s (labctl pki sub-ca)", o.SubCA)
		}
		caCrt, caKey, bare = filepath.Join(sub, "ca.crt"), filepath.Join(sub, "ca.key"), false
		if b, err := os.ReadFile(filepath.Join(sub, "domain")); err == nil {
			domain = strings.TrimSpace(string(b))
		}
	}
	if o.Domain != "" {
		domain = o.Domain
	}
	ca, signer, err := loadCA(caCrt, caKey)
	if err != nil {
		return err
	}

	// Noms : NOM (+ NOM.domaine si NOM n'est pas un FQDN ; pas de nom court sous une CA dérivée,
	// il serait refusé par sa contrainte de nom), puis les ajouts
	tmpl := &x509.Certificate{
		Subject:               pkix.Name{Organization: []string{p.Org}, CommonName: o.Name},
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
	}
	if o.Client {
		tmpl.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}
	}
	if strings.Contains(o.Name, ".") {
		tmpl.DNSNames = append(tmpl.DNSNames, o.Name)
	} else {
		if bare {
			tmpl.DNSNames = append(tmpl.DNSNames, o.Name)
		}
		tmpl.DNSNames = append(tmpl.DNSNames, o.Name+"."+domain)
	}
	for _, d := range o.DNS {
		if d = strings.TrimSpace(d); d != "" {
			tmpl.DNSNames = append(tmpl.DNSNames, d)
		}
	}
	for _, s := range o.IPs {
		if s = strings.TrimSpace(s); s == "" {
			continue
		}
		ip := net.ParseIP(s)
		if ip == nil {
			return fmt.Errorf("adresse IP invalide : %s", s)
		}
		tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
	}

	days := o.Days
	if days <= 0 {
		days = DefaultDays
	}
	key, err := rsa.GenerateKey(rand.Reader, p.bits(2048))
	if err != nil {
		return err
	}
	der, err := p.sign(tmpl, ca, &key.PublicKey, signer, days)
	if err != nil {
		return err
	}
	leaf, _ := x509.ParseCertificate(der)
	if err := p.verify(leaf, ca); err != nil {
		return fmt.Errorf("certificat refusé à la vérification : %w", err)
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	base := filepath.Join(dir, o.Name)
	if err := writeKey(base+".key", key); err != nil {
		return err
	}
	if err := writeCerts(base+".crt", der); err != nil {
		return err
	}
	if err := writeCerts(base+".fullchain.crt", der, ca.Raw); err != nil {
		return err
	}
	if o.PFX {
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
	p.logf("ok  certificat %s (%s) -> %s", o.Name, strings.Join(SANs(leaf), ","), dir)
	return nil
}

// verify contrôle la chaîne jusqu'à la racine, contraintes de nom comprises.
func (p *PKI) verify(leaf, issuer *x509.Certificate) error {
	root, err := readCert(p.rootCrt())
	if err != nil {
		return err
	}
	roots, inter := x509.NewCertPool(), x509.NewCertPool()
	roots.AddCert(root)
	if !issuer.Equal(root) {
		inter.AddCert(issuer)
	}
	_, err = leaf.Verify(x509.VerifyOptions{Roots: roots, Intermediates: inter, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageAny}})
	return err
}

// SANs renvoie les noms d'un certificat au format « DNS:x » / « IP:y ».
func SANs(c *x509.Certificate) []string {
	var out []string
	for _, d := range c.DNSNames {
		out = append(out, "DNS:"+d)
	}
	for _, ip := range c.IPAddresses {
		out = append(out, "IP:"+ip.String())
	}
	return out
}

// ---------------------------------------------------------------- outils communs

// sign signe tmpl avec la clé de l'émetteur : numéro de série aléatoire de 128 bits, identifiants de clé.
func (p *PKI) sign(tmpl, issuer *x509.Certificate, pub *rsa.PublicKey, signer crypto.Signer, days int) ([]byte, error) {
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, err
	}
	tmpl.SerialNumber = serial
	tmpl.NotBefore = time.Now().Add(-time.Minute)
	tmpl.NotAfter = time.Now().AddDate(0, 0, days)
	tmpl.SubjectKeyId = keyID(pub)
	tmpl.SignatureAlgorithm = x509.SHA256WithRSA
	return x509.CreateCertificate(rand.Reader, tmpl, issuer, pub, signer)
}

// keyID calcule l'identifiant de clé (SHA-1 de la clé publique, méthode 1 de la RFC 5280).
func keyID(pub *rsa.PublicKey) []byte {
	der := x509.MarshalPKCS1PublicKey(pub)
	sum := sha1.Sum(der)
	return sum[:]
}

func loadCA(crtPath, keyPath string) (*x509.Certificate, crypto.Signer, error) {
	crt, err := readCert(crtPath)
	if err != nil {
		return nil, nil, err
	}
	b, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, nil, err
	}
	blk, _ := pem.Decode(b)
	if blk == nil {
		return nil, nil, fmt.Errorf("%s : clé PEM illisible", keyPath)
	}
	var key any
	switch blk.Type {
	case "PRIVATE KEY":
		key, err = x509.ParsePKCS8PrivateKey(blk.Bytes)
	case "RSA PRIVATE KEY":
		key, err = x509.ParsePKCS1PrivateKey(blk.Bytes)
	default:
		err = fmt.Errorf("type de clé non pris en charge : %s", blk.Type)
	}
	if err != nil {
		return nil, nil, fmt.Errorf("%s : %w", keyPath, err)
	}
	signer, ok := key.(crypto.Signer)
	if !ok {
		return nil, nil, fmt.Errorf("%s : clé inutilisable pour signer", keyPath)
	}
	return crt, signer, nil
}

func readCert(path string) (*x509.Certificate, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	blk, _ := pem.Decode(b)
	if blk == nil || blk.Type != "CERTIFICATE" {
		return nil, fmt.Errorf("%s : pas de certificat PEM", path)
	}
	return x509.ParseCertificate(blk.Bytes)
}

// ReadCert lit un certificat PEM (premier bloc du fichier).
func ReadCert(path string) (*x509.Certificate, error) { return readCert(path) }

func writeKey(path string, key *rsa.PrivateKey) error {
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return err
	}
	return os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), 0o600)
}

func writeCerts(path string, ders ...[]byte) error {
	var buf []byte
	for _, d := range ders {
		buf = append(buf, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: d})...)
	}
	return os.WriteFile(path, buf, 0o644)
}
