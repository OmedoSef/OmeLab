package pki

import (
	"archive/tar"
	"bytes"
	"crypto/x509"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"
	"time"
)

// ---------------------------------------------------------------- list / show

// List affiche les CA dérivées puis les certificats émis, avec leur expiration.
func (p *PKI) List(w io.Writer) error {
	if !p.Initialized() {
		return ErrNotInitialized
	}
	tw := tabwriter.NewWriter(w, 0, 0, 3, ' ', 0)
	subs, _ := os.ReadDir(p.path("sub-ca"))
	for _, s := range subs {
		c, err := readCert(p.path("sub-ca", s.Name(), "ca.crt"))
		if err != nil {
			continue
		}
		dom, _ := os.ReadFile(p.path("sub-ca", s.Name(), "domain"))
		fmt.Fprintf(tw, "[CA] %s\t%s\t%s\n", s.Name(), expiry(c), strings.TrimSpace(string(dom)))
	}
	fmt.Fprintln(tw, "NOM\tEXPIRE\tSAN")
	for _, name := range p.CertNames() {
		c, err := readCert(p.path("certs", name, name+".crt"))
		if err != nil {
			continue
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\n", name, expiry(c), strings.Join(SANs(c), ", "))
	}
	return tw.Flush()
}

// Show affiche le détail d'un certificat émis.
func (p *PKI) Show(w io.Writer, name string) error {
	c, err := readCert(p.path("certs", name, name+".crt"))
	if err != nil {
		return fmt.Errorf("certificat introuvable : %s", name)
	}
	usages := []string{}
	for _, u := range c.ExtKeyUsage {
		switch u {
		case x509.ExtKeyUsageServerAuth:
			usages = append(usages, "serveur TLS")
		case x509.ExtKeyUsageClientAuth:
			usages = append(usages, "client TLS")
		}
	}
	fmt.Fprintf(w, "sujet      : %s\n", c.Subject)
	fmt.Fprintf(w, "émetteur   : %s\n", c.Issuer)
	fmt.Fprintf(w, "validité   : %s -> %s\n", c.NotBefore.Format(time.DateOnly), expiry(c))
	fmt.Fprintf(w, "noms (SAN) : %s\n", strings.Join(SANs(c), ", "))
	fmt.Fprintf(w, "usages     : %s\n", strings.Join(usages, ", "))
	fmt.Fprintf(w, "fichiers   : %s\n", p.path("certs", name))
	return nil
}

// CertNames liste les certificats émis.
func (p *PKI) CertNames() []string { return dirNames(p.path("certs")) }

// SubCANames liste les CA dérivées.
func (p *PKI) SubCANames() []string { return dirNames(p.path("sub-ca")) }

func dirNames(dir string) []string {
	var out []string
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if e.IsDir() {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out
}

func expiry(c *x509.Certificate) string {
	return fmt.Sprintf("%s (%d j)", c.NotAfter.Format(time.DateOnly), int(time.Until(c.NotAfter).Hours()/24))
}

// ---------------------------------------------------------------- VMs et hôte

// Remote donne accès aux VMs par SSH (adresse IP résolue par l'appelant).
type Remote struct {
	IP      func(vm string) (string, error)
	User    string
	SSHOpts []string
}

func (r Remote) ssh(vm, script string, stdin io.Reader) error {
	ip, err := r.IP(vm)
	if err != nil {
		return fmt.Errorf("%s : %w", vm, err)
	}
	args := append(append([]string{}, r.SSHOpts...), r.User+"@"+ip, script)
	cmd := exec.Command("ssh", args...)
	cmd.Stdin = stdin
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s : %v %s", vm, err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// sshOut exécute un script dans la VM et renvoie sa sortie standard (pour sonder, sans rien modifier).
func (r Remote) sshOut(vm, script string) ([]byte, error) {
	ip, err := r.IP(vm)
	if err != nil {
		return nil, fmt.Errorf("%s : %w", vm, err)
	}
	args := append(append([]string{}, r.SSHOpts...), r.User+"@"+ip, script)
	cmd := exec.Command("ssh", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("%s : %v %s", vm, err, strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
}

// Script exécuté dans la VM : installe la CA lue sur l'entrée standard (Debian/Ubuntu ou famille RHEL)
const remoteTrust = `set -e
if [ -d /usr/local/share/ca-certificates ]; then
  sudo tee /usr/local/share/ca-certificates/labs-root-ca.crt >/dev/null && sudo update-ca-certificates >/dev/null 2>&1
elif [ -d /etc/pki/ca-trust/source/anchors ]; then
  sudo tee /etc/pki/ca-trust/source/anchors/labs-root-ca.crt >/dev/null && sudo update-ca-trust
else echo "magasin de certificats non reconnu" >&2; exit 1; fi`

// TrustVM installe la CA racine dans le magasin système des VMs. Continue après une VM en échec.
func (p *PKI) TrustVM(r Remote, vms ...string) error {
	root, err := os.ReadFile(p.rootCrt())
	if err != nil {
		return ErrNotInitialized
	}
	var failed []string
	for _, vm := range vms {
		if err := r.ssh(vm, remoteTrust, bytes.NewReader(root)); err != nil {
			p.logf("attention: %v", err)
			failed = append(failed, vm)
			continue
		}
		p.logf("ok  %s fait confiance à la CA racine", vm)
	}
	if len(failed) > 0 {
		return fmt.Errorf("échec pour : %s", strings.Join(failed, ", "))
	}
	return nil
}

// Deploy copie clé, certificat, chaîne complète et racine d'un certificat émis dans la VM (dossier dest).
func (p *PKI) Deploy(r Remote, vm, name, dest string) error {
	if dest == "" {
		dest = "/etc/ssl/lab"
	}
	if !strings.HasPrefix(dest, "/") || strings.ContainsAny(dest, "'\"`$\\ \n") {
		return fmt.Errorf("dossier de destination invalide : %s", dest)
	}
	dir := p.path("certs", name)
	files := map[string]string{
		name + ".key":           filepath.Join(dir, name+".key"),
		name + ".crt":           filepath.Join(dir, name+".crt"),
		name + ".fullchain.crt": filepath.Join(dir, name+".fullchain.crt"),
		"root-ca.crt":           p.rootCrt(),
	}
	if _, err := os.Stat(files[name+".crt"]); err != nil {
		return fmt.Errorf("certificat introuvable : %s (labctl pki issue %s)", name, name)
	}
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, n := range []string{name + ".key", name + ".crt", name + ".fullchain.crt", "root-ca.crt"} {
		b, err := os.ReadFile(files[n])
		if err != nil {
			return err
		}
		mode := int64(0o644)
		if strings.HasSuffix(n, ".key") {
			mode = 0o600
		}
		if err := tw.WriteHeader(&tar.Header{Name: n, Mode: mode, Size: int64(len(b)), ModTime: time.Now()}); err != nil {
			return err
		}
		if _, err := tw.Write(b); err != nil {
			return err
		}
	}
	if err := tw.Close(); err != nil {
		return err
	}
	script := fmt.Sprintf("sudo mkdir -p '%s' && sudo tar -C '%s' --no-same-owner -xf - && sudo chmod 600 '%s/%s.key'", dest, dest, dest, name)
	if err := r.ssh(vm, script, &buf); err != nil {
		return err
	}
	p.logf("ok  %s déployé sur %s dans %s", name, vm, dest)
	return nil
}

// TrustHost installe la CA racine dans le magasin de cet hôte (Debian/Ubuntu ; sudo, interactif).
func (p *PKI) TrustHost() error {
	if !p.Initialized() {
		return ErrNotInitialized
	}
	for _, args := range [][]string{
		{"install", "-m", "0644", p.rootCrt(), "/usr/local/share/ca-certificates/labs-root-ca.crt"},
		{"update-ca-certificates"},
	} {
		cmd := exec.Command("sudo", args...)
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("sudo %s : %w", args[0], err)
		}
	}
	p.logf("ok  CA racine installée sur l'hôte (navigateurs : importer %s dans leur magasin si besoin)", p.rootCrt())
	return nil
}
