package pki

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"os"
	"strings"
)

// Autorité d'interception : racine à part (hors de la chaîne Omedo Labs), utilisée par le proxy de la passerelle
// (mitmproxy, connexions via: proxy en mode intercept) pour signer à la volée un certificat pour n'importe quel
// site. Elle n'est installée que dans les VMs choisies (TrustInterceptionVM) : les autres voient des erreurs de
// certificat, ce qui fait partie de ce qu'on observe. Sa clé ne sort de pki/ que vers la passerelle.

func (p *PKI) interceptionCrt() string { return p.path("interception", "interception-ca.crt") }
func (p *PKI) interceptionKey() string { return p.path("interception", "interception-ca.key") }

// InterceptionCA crée l'autorité d'interception si elle n'existe pas, et renvoie son certificat (PEM) et le
// fichier attendu par mitmproxy (clé puis certificat, PEM).
func (p *PKI) InterceptionCA() (cert, bundle []byte, err error) {
	if _, err := os.Stat(p.interceptionCrt()); os.IsNotExist(err) {
		if err := os.MkdirAll(p.path("interception"), 0o700); err != nil {
			return nil, nil, err
		}
		p.logf("==> autorité d'interception (racine à part, pour le proxy de la passerelle)")
		key, err := rsa.GenerateKey(rand.Reader, p.bits(3072))
		if err != nil {
			return nil, nil, err
		}
		tmpl := &x509.Certificate{
			Subject:               pkix.Name{Organization: []string{p.Org}, CommonName: p.Org + " Interception CA"},
			BasicConstraintsValid: true, IsCA: true, MaxPathLenZero: true,
			KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature,
		}
		der, err := p.sign(tmpl, tmpl, &key.PublicKey, key, caDays)
		if err != nil {
			return nil, nil, err
		}
		if err := writeKey(p.interceptionKey(), key); err != nil {
			return nil, nil, err
		}
		if err := writeCerts(p.interceptionCrt(), der); err != nil {
			return nil, nil, err
		}
		p.logf("ok  autorité d'interception : %s", p.interceptionCrt())
	}
	cert, err = os.ReadFile(p.interceptionCrt())
	if err != nil {
		return nil, nil, err
	}
	key, err := os.ReadFile(p.interceptionKey())
	if err != nil {
		return nil, nil, err
	}
	if b, _ := pem.Decode(cert); b == nil || b.Type != "CERTIFICATE" {
		return nil, nil, fmt.Errorf("%s illisible", p.interceptionCrt())
	}
	return cert, append(append(append([]byte{}, key...), '\n'), cert...), nil
}

// TrustInterceptionVM installe l'autorité d'interception dans le magasin système des VMs (le proxy en mode
// intercept devient invisible pour elles). Continue après une VM en échec.
func (p *PKI) TrustInterceptionVM(r Remote, vms ...string) error {
	cert, _, err := p.InterceptionCA()
	if err != nil {
		return err
	}
	script := strings.ReplaceAll(remoteTrust, "labs-root-ca.crt", "labs-interception-ca.crt")
	var failed []string
	for _, vm := range vms {
		if err := r.ssh(vm, script, bytes.NewReader(cert)); err != nil {
			p.logf("attention: %v", err)
			failed = append(failed, vm)
			continue
		}
		p.logf("ok  %s fait confiance à l'autorité d'interception", vm)
	}
	if len(failed) > 0 {
		return fmt.Errorf("échec pour : %s", strings.Join(failed, ", "))
	}
	return nil
}
