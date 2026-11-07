package lab

import (
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"labs/labctl/internal/pki"
)

// PKITool renvoie la PKI des labs (réglages LABS_PKI_*), messages écrits sur out.
func (l *Lab) PKITool(out io.Writer) *pki.PKI {
	return &pki.PKI{
		Dir:    l.PKIDir(),
		Org:    l.Org(),
		Domain: l.RootDomain(),
		Out:    out,
	}
}

// PKIDir renvoie le dossier de la PKI (LABS_PKI_DIR, sinon <racine>/pki).
func (l *Lab) PKIDir() string { return l.Setting("LABS_PKI_DIR", filepath.Join(l.Root, "pki")) }

// Remote construit l'accès SSH aux VMs (poser ou sonder les CA). BatchMode : aucune invite, on échoue vite ;
// VMIP vide (VM éteinte) → erreur immédiate, pas d'attente.
func (l *Lab) Remote() pki.Remote {
	return pki.Remote{
		User:    l.Setting("LABS_VM_USER", "lab"),
		SSHOpts: l.SSHArgs(true), // options SSH communes (lab/ssh.go)
		IP: func(vm string) (string, error) {
			if ip := l.VMIP(vm); ip != "" {
				return ip, nil
			}
			return "", fmt.Errorf("pas d'IP (VM démarrée ?)")
		},
	}
}

// TrustStatus sonde la confiance des VMs envers la racine et l'autorité d'interception. À la demande (bouton),
// jamais au rafraîchissement : un aller-retour SSH par VM.
func (l *Lab) TrustStatus(vms ...string) map[string]pki.VMTrust {
	return l.PKITool(io.Discard).TrustStatus(l.Remote(), vms...)
}

// PKIState décrit la PKI telle qu'elle est sur disque (lue directement dans les certificats).
type PKIState struct {
	Initialized  bool       `json:"initialized"`
	Org          string     `json:"org"`                    // organisation du lab (LABS_PKI_ORG) : nom des CA
	Domain       string     `json:"domain"`                 // domaine racine (LABS_PKI_DOMAIN)
	CAs          []CertInfo `json:"cas"`                    // racine, intermédiaire, CA dérivées
	Interception *CertInfo  `json:"interception,omitempty"` // autorité du proxy (mode intercept), racine à part ; absente tant que non générée
	Certs        []CertInfo `json:"certs"`                  // certificats émis (certs/<nom>/<nom>.crt)
	Archives     []string   `json:"archives"`               // archives créées avant chaque renouvellement
}

// CertInfo résume un certificat.
type CertInfo struct {
	Name        string   `json:"name"`
	Kind        string   `json:"kind"` // root, intermediate, sub-ca, server, client
	Subject     string   `json:"subject"`
	Issuer      string   `json:"issuer"`
	NotAfter    string   `json:"notAfter"`
	DaysLeft    int      `json:"daysLeft"`
	SANs        []string `json:"sans,omitempty"`
	Constraints []string `json:"constraints,omitempty"` // nameConstraints d'une CA dérivée
	Domain      string   `json:"domain,omitempty"`      // domaine d'une CA dérivée
	Files       []string `json:"files"`                 // fichiers publics téléchargeables (relatifs à pki/)
	HasPFX      bool     `json:"hasPfx,omitempty"`
}

// PKI lit l'état de la PKI.
func (l *Lab) PKI() (*PKIState, error) {
	dir := l.PKIDir()
	st := &PKIState{Org: l.Org(), Domain: l.RootDomain()}
	add := func(list *[]CertInfo, name, kind, rel string, extra ...string) {
		c, err := readCert(filepath.Join(dir, rel))
		if err != nil {
			return
		}
		info := certInfo(name, kind, c)
		info.Files = append([]string{rel}, existing(dir, extra...)...)
		*list = append(*list, info)
	}
	if _, err := os.Stat(filepath.Join(dir, "root", "root-ca.crt")); err != nil {
		return st, nil
	}
	st.Initialized = true
	add(&st.CAs, "racine", "root", "root/root-ca.crt")
	add(&st.CAs, "intermédiaire", "intermediate", "intermediate/intermediate-ca.crt", "intermediate/ca-chain.crt")

	subs, _ := os.ReadDir(filepath.Join(dir, "sub-ca"))
	for _, s := range subs {
		n := len(st.CAs)
		add(&st.CAs, s.Name(), "sub-ca", filepath.Join("sub-ca", s.Name(), "ca.crt"), filepath.Join("sub-ca", s.Name(), "ca-chain.crt"))
		if len(st.CAs) > n {
			if b, err := os.ReadFile(filepath.Join(dir, "sub-ca", s.Name(), "domain")); err == nil {
				st.CAs[n].Domain = strings.TrimSpace(string(b))
			}
		}
	}

	// Autorité d'interception du proxy (racine à part) : présente seulement si elle a déjà été générée.
	if c, err := readCert(filepath.Join(dir, "interception", "interception-ca.crt")); err == nil {
		info := certInfo("interception", "interception", c)
		info.Files = []string{"interception/interception-ca.crt"}
		st.Interception = &info
	}

	if arch, err := os.ReadDir(filepath.Join(dir, "archive")); err == nil {
		for _, a := range arch {
			st.Archives = append(st.Archives, a.Name())
		}
	}
	certs, _ := os.ReadDir(filepath.Join(dir, "certs"))
	for _, c := range certs {
		name := c.Name()
		base := filepath.Join("certs", name, name)
		n := len(st.Certs)
		add(&st.Certs, name, "server", base+".crt", base+".fullchain.crt")
		if len(st.Certs) > n {
			if _, err := os.Stat(filepath.Join(dir, base+".p12")); err == nil {
				st.Certs[n].HasPFX = true
			}
		}
	}
	return st, nil
}

// SubCANames liste les CA dérivées (pour la complétion de -C).
func (l *Lab) SubCANames() []string {
	var out []string
	entries, _ := os.ReadDir(filepath.Join(l.PKIDir(), "sub-ca"))
	for _, e := range entries {
		if e.IsDir() {
			out = append(out, e.Name())
		}
	}
	return out
}

// CertNames liste les certificats émis.
func (l *Lab) CertNames() []string {
	var out []string
	entries, _ := os.ReadDir(filepath.Join(l.PKIDir(), "certs"))
	for _, e := range entries {
		if e.IsDir() {
			out = append(out, e.Name())
		}
	}
	return out
}

// PublicFile renvoie le chemin absolu d'un fichier public de la PKI (certificat, jamais de clé),
// ou une erreur si le chemin demandé sort de pki/ ou n'est pas un certificat.
func (l *Lab) PublicFile(rel string) (string, error) {
	clean := filepath.Clean(rel)
	if filepath.IsAbs(clean) || strings.HasPrefix(clean, "..") || !strings.HasSuffix(clean, ".crt") {
		return "", fmt.Errorf("fichier non autorisé : %s", rel)
	}
	p := filepath.Join(l.PKIDir(), clean)
	b, err := os.ReadFile(p)
	if err != nil {
		return "", err
	}
	if strings.Contains(string(b), "PRIVATE KEY") {
		return "", fmt.Errorf("fichier non autorisé : %s", rel)
	}
	return p, nil
}

func existing(dir string, rels ...string) []string {
	var out []string
	for _, r := range rels {
		if _, err := os.Stat(filepath.Join(dir, r)); err == nil {
			out = append(out, r)
		}
	}
	return out
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

func certInfo(name, kind string, c *x509.Certificate) CertInfo {
	info := CertInfo{
		Name: name, Kind: kind,
		Subject:  c.Subject.CommonName,
		Issuer:   c.Issuer.CommonName,
		NotAfter: c.NotAfter.Format("2006-01-02"),
		DaysLeft: int(time.Until(c.NotAfter).Hours() / 24),
	}
	if kind == "server" {
		client, server := false, false
		for _, u := range c.ExtKeyUsage {
			client = client || u == x509.ExtKeyUsageClientAuth
			server = server || u == x509.ExtKeyUsageServerAuth
		}
		if client && !server {
			info.Kind = "client"
		}
	}
	info.SANs = append(info.SANs, c.DNSNames...)
	for _, ip := range c.IPAddresses {
		info.SANs = append(info.SANs, ip.String())
	}
	for _, d := range c.PermittedDNSDomains {
		info.Constraints = append(info.Constraints, "DNS "+d)
	}
	for _, n := range c.PermittedIPRanges {
		info.Constraints = append(info.Constraints, "IP "+ipNet(n))
	}
	for _, n := range c.ExcludedIPRanges {
		info.Constraints = append(info.Constraints, "IP exclues "+ipNet(n))
	}
	sort.Strings(info.Constraints)
	return info
}

func ipNet(n *net.IPNet) string {
	ones, _ := n.Mask.Size()
	return fmt.Sprintf("%s/%d", n.IP, ones)
}

// AllVMNames liste les noms libvirt des VMs des modules, plus la passerelle (cibles de trust-vm / deploy).
func (l *Lab) AllVMNames() []string {
	out := []string{GatewayVM}
	mods, _ := l.Modules()
	for _, m := range mods {
		for _, vm := range l.VMs(m.Name) {
			out = append(out, m.LibvirtName(vm))
		}
	}
	return out
}
