package lab

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"labs/labctl/internal/module"
	"labs/labctl/internal/pki"
	"labs/labctl/internal/virt"
)

// Moteur des modules : certificats des VMs émis par la CA du module (voir moduleup.go).

// ---------------------------------------------------------------- certificats

// certWant renvoie les noms DNS et IP attendus d'un certificat déclaré.
func (r *run) certWant(vm string, c module.Cert) (dns, ips []string) {
	dom := r.domain()
	for _, d := range c.DNS {
		if !strings.Contains(d, ".") {
			d += "." + dom
		}
		dns = append(dns, d)
	}
	if c.IP {
		if ip := r.vmIP(vm); ip != "" {
			ips = append(ips, ip)
		}
	}
	return dns, ips
}

// certStale indique pourquoi un certificat émis doit être réémis ("" s'il est bon).
func (r *run) certStale(path string, c module.Cert, dns, ips []string) string {
	leaf, err := pki.ReadCert(path)
	if err != nil {
		return "illisible"
	}
	ca, err := pki.ReadCert(filepath.Join(r.l.PKIDir(), "sub-ca", r.y.PKI.CA, "ca.crt"))
	if err != nil {
		return "CA du module illisible"
	}
	if leaf.CheckSignatureFrom(ca) != nil {
		return "émis par une autre CA (PKI renouvelée)"
	}
	if time.Until(leaf.NotAfter) < 30*24*time.Hour {
		return "expire dans moins de 30 jours"
	}
	have := pki.SANs(leaf)
	want := []string{"DNS:" + c.Name + "." + r.domain()}
	for _, d := range dns {
		want = append(want, "DNS:"+d)
	}
	for _, ip := range ips {
		want = append(want, "IP:"+ip)
	}
	for _, w := range want {
		if !slices.Contains(have, w) {
			return "nom manquant : " + strings.TrimPrefix(strings.TrimPrefix(w, "DNS:"), "IP:")
		}
	}
	return ""
}

// ensureCert émet (ou réémet si renew ou s'il n'est plus valable) un certificat déclaré ; renvoie les
// chemins de la chaîne complète et de la clé.
func (r *run) ensureCert(vm string, c module.Cert, renew bool) (crt, key string, err error) {
	dir := filepath.Join(r.l.PKIDir(), "certs", c.Name)
	crt, key = filepath.Join(dir, c.Name+".fullchain.crt"), filepath.Join(dir, c.Name+".key")
	dns, ips := r.certWant(vm, c)
	if _, err := os.Stat(filepath.Join(dir, c.Name+".crt")); err == nil {
		why := r.certStale(filepath.Join(dir, c.Name+".crt"), c, dns, ips)
		if why == "" && !renew {
			return crt, key, nil
		}
		if why == "" {
			why = "demandé"
		}
		r.logf("==> Réémission du certificat %s (%s)", c.Name, why)
		old := dir + ".old"
		os.RemoveAll(old)
		if err := os.Rename(dir, old); err != nil {
			return "", "", err
		}
		defer func() {
			if err != nil {
				os.RemoveAll(dir)
				_ = os.Rename(old, dir)
			} else {
				os.RemoveAll(old)
			}
		}()
	}
	err = r.l.PKITool(r.out).Issue(pki.IssueOptions{Name: c.Name, SubCA: r.y.PKI.CA, DNS: dns, IPs: ips})
	return crt, key, err
}

// installCerts installe les certificats déclarés d'une VM (par SSH) s'ils diffèrent de ceux en place,
// puis lance la commande reload.
func (r *run) installCerts(vm string, renew bool) error {
	e := r.eff(vm)
	ip := r.vmIP(vm)
	if ip == "" {
		return fmt.Errorf("%s : pas d'IP (VM démarrée ?)", vm)
	}
	target := r.l.VMUser() + "@" + ip
	ssh := func(script string, stdin io.Reader) ([]byte, error) {
		stdout, stderr, err := r.l.shell(target, script, stdin)
		return append(stdout, stderr...), err
	}
	vars := module.VarContext{Module: r.y, VM: vm, Root: r.l.pkiDomain(), Keyboard: r.l.Keyboard(), LabVars: r.l.LabVars()}
	for _, c := range e.VM.Certificates {
		crt, key, err := r.ensureCert(vm, c, renew)
		if err != nil {
			return err
		}
		crtB, err1 := os.ReadFile(crt)
		keyB, err2 := os.ReadFile(key)
		if err := errors.Join(err1, err2); err != nil {
			return err
		}
		sum := func(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
		have, err := ssh(fmt.Sprintf("sudo sha256sum %s %s 2>/dev/null | cut -d' ' -f1", shellQuote(c.Cert), shellQuote(c.Key)), nil)
		if err != nil {
			return fmt.Errorf("%s injoignable en SSH (%s) : %s", vm, target, strings.TrimSpace(string(have)))
		}
		if slices.Equal(strings.Fields(string(have)), []string{sum(crtB), sum(keyB)}) {
			r.logf("ok  certificat %s à jour dans %s", c.Name, vm)
			continue
		}
		// Les deux fichiers en un seul envoi, séparés par une ligne repère
		payload := bytes.Join([][]byte{crtB, []byte("--8<-- labctl --8<--\n"), keyB}, nil)
		script := fmt.Sprintf(`set -e; t=$(mktemp -d); csplit -s -f "$t/p" - '/^--8<-- labctl --8<--$/'
sed -i 1d "$t/p01"
sudo install -D -m 0644 "$t/p00" %s; sudo install -D -m 0600 "$t/p01" %s; rm -rf "$t"`, shellQuote(c.Cert), shellQuote(c.Key)) // %q n'est pas un échappement shell
		if b, err := ssh(script, bytes.NewReader(payload)); err != nil {
			return fmt.Errorf("installation du certificat %s : %s", c.Name, strings.TrimSpace(string(b)))
		}
		r.logf("ok  certificat %s installé dans %s", c.Name, vm)
		if c.Reload != "" {
			cmd, err := vars.Expand(c.Reload)
			if err != nil {
				return err
			}
			if b, err := ssh("sudo sh -c "+shellQuote(cmd), nil); err != nil {
				return fmt.Errorf("reload (%s) : %s", cmd, strings.TrimSpace(string(b)))
			}
			r.logf("ok  %s : %s", vm, cmd)
		}
	}
	return nil
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// ModuleCerts (ré)émet et réinstalle les certificats déclarés des VMs démarrées.
func (l *Lab) ModuleCerts(name string, vms []string, renew bool, out io.Writer) error {
	r, err := l.loadRun(name, out)
	if err != nil {
		return err
	}
	if vms, err = r.selectVMs(vms); err != nil {
		return err
	}
	n := 0
	var errs []error
	for _, vm := range vms {
		if len(r.y.VMs[vm].Certificates) == 0 {
			continue
		}
		n++
		if l.DomState(r.libvirtName(vm)) != virt.StateRunning {
			for _, c := range r.y.VMs[vm].Certificates {
				if _, _, err := r.ensureCert(vm, c, renew); err != nil {
					errs = append(errs, err)
				}
			}
			r.logf("attention: %s arrêtée : certificats prêts, installés au prochain « up » ou « certs »", vm)
			continue
		}
		if err := r.installCerts(vm, renew); err != nil {
			errs = append(errs, err)
		}
	}
	if n == 0 {
		r.logf("aucun certificat déclaré")
	}
	return errors.Join(errs...)
}
