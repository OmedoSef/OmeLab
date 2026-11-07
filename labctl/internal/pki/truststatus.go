package pki

import (
	"bytes"
	"encoding/base64"
	"encoding/pem"
	"os"
	"sync"
)

// VMTrust : état de confiance d'une VM vis-à-vis des autorités du lab, sondé en direct (lecture seule).
type VMTrust struct {
	Reachable    bool   `json:"reachable"`     // la VM a répondu (démarrée, joignable en SSH)
	Root         bool   `json:"root"`          // la CA racine est dans le magasin système
	Interception bool   `json:"interception"`  // l'autorité d'interception y est aussi
	Err          string `json:"err,omitempty"` // injoignable : raison (éteinte, pas d'IP, SSH refusé…)
}

// catBundle lit les magasins de CA système (Debian/Ubuntu et famille RHEL) ; || true pour ne pas échouer si
// l'un des deux fichiers manque.
const catBundle = `cat /etc/ssl/certs/ca-certificates.crt /etc/pki/tls/certs/ca-bundle.crt 2>/dev/null || true`

// TrustStatus sonde chaque VM en parallèle et dit si elle fait confiance à la CA racine et à l'autorité
// d'interception, en cherchant l'empreinte (corps base64) de chaque certificat dans le magasin système. Un
// simple `cat` du bundle côté VM ; la comparaison se fait ici. À appeler à la demande, pas en boucle : c'est un
// aller-retour SSH par VM. Une VM injoignable est signalée (Err), jamais une erreur globale.
func (p *PKI) TrustStatus(r Remote, vms ...string) map[string]VMTrust {
	rootBody := pemBase64(readFileQuiet(p.rootCrt()))
	intBody := pemBase64(readFileQuiet(p.interceptionCrt()))
	out := make(map[string]VMTrust, len(vms))
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, vm := range vms {
		wg.Add(1)
		go func(vm string) {
			defer wg.Done()
			t := p.probeTrust(r, vm, rootBody, intBody)
			mu.Lock()
			out[vm] = t
			mu.Unlock()
		}(vm)
	}
	wg.Wait()
	return out
}

func (p *PKI) probeTrust(r Remote, vm string, rootBody, intBody []byte) VMTrust {
	bundle, err := r.sshOut(vm, catBundle)
	if err != nil {
		return VMTrust{Err: err.Error()}
	}
	flat := stripSpace(bundle)
	t := VMTrust{Reachable: true}
	if len(rootBody) > 0 {
		t.Root = bytes.Contains(flat, rootBody)
	}
	if len(intBody) > 0 {
		t.Interception = bytes.Contains(flat, intBody)
	}
	return t
}

func readFileQuiet(path string) []byte { b, _ := os.ReadFile(path); return b }

// pemBase64 renvoie le corps base64 (sans en-tête ni retours) du 1er bloc CERTIFICATE. C'est la forme
// canonique : une fois les blancs retirés du bundle, ce corps s'y retrouve tel quel si le certificat est présent
// (le découpage en lignes de 64 n'y change rien).
func pemBase64(p []byte) []byte {
	b, _ := pem.Decode(p)
	if b == nil {
		return nil
	}
	return []byte(base64.StdEncoding.EncodeToString(b.Bytes))
}

func stripSpace(b []byte) []byte {
	out := make([]byte, 0, len(b))
	for _, c := range b {
		if c != ' ' && c != '\t' && c != '\n' && c != '\r' {
			out = append(out, c)
		}
	}
	return out
}
