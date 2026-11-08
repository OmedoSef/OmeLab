package lab

import (
	"archive/tar"
	"bytes"
	"embed"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"labs/labctl/internal/coreconf"
	"labs/labctl/internal/virt"
)

// La passerelle core-gw (DHCP, DNS, Internet et liens des modules) est intégrée à labctl : ce n'est pas
// un module. Ses fichiers système sont embarqués dans l'exécutable ; un fichier du même nom dans
// <racine>/core/files/ le remplace (mise au point sans recompiler).

//go:embed gwfiles
var gwFiles embed.FS

// gwFile est un fichier installé sur la passerelle.
type gwFile struct{ name, dest, mode string }

var gwInstall = []gwFile{
	{"gwctl", "/usr/local/sbin/gwctl", "0755"},
	{"gwdhcp-hook", "/usr/local/sbin/gwdhcp-hook", "0755"},
	{"gw.service", "/etc/systemd/system/gw.service", "0644"},
	{"gwdns@.service", "/etc/systemd/system/gwdns@.service", "0644"},
	{"gwportal.service", "/etc/systemd/system/gwportal.service", "0644"},
	{"gwproxy.service", "/etc/systemd/system/gwproxy.service", "0644"},
	{"gwproxy.py", "/usr/local/lib/gwproxy/gwproxy.py", "0644"},
	{"90-gw.conf", "/etc/sysctl.d/90-gw.conf", "0644"},
}

// GatewayFile renvoie le contenu d'un fichier de la passerelle (remplacement local, sinon embarqué).
func (l *Lab) GatewayFile(name string) ([]byte, error) {
	if b, err := os.ReadFile(filepath.Join(l.Root, "core", "files", name)); err == nil {
		return b, nil
	}
	return gwFiles.ReadFile("gwfiles/" + name)
}

// Gateway pilote la passerelle ; les messages sont écrits sur Out.
type Gateway struct {
	l   *Lab
	Out io.Writer
}

// Gateway renvoie le pilote de la passerelle.
func (l *Lab) Gateway(out io.Writer) *Gateway {
	if out == nil {
		out = io.Discard
	}
	return &Gateway{l: l, Out: out}
}

func (g *Gateway) logf(format string, a ...any) { fmt.Fprintf(g.Out, format+"\n", a...) }

// Running indique si la passerelle tourne.
func (g *Gateway) Running() bool { return g.l.DomState(GatewayVM) == virt.StateRunning }

func (g *Gateway) require() error {
	switch g.l.DomState(GatewayVM) {
	case virt.StateRunning:
		return nil
	case virt.StateAbsent:
		return errors.New("passerelle absente : labctl core gw")
	}
	return errors.New("passerelle arrêtée : labctl core start")
}

// target renvoie utilisateur@IP de la passerelle (sur le réseau NAT libvirt).
func (g *Gateway) target() (string, error) {
	ip := g.l.Hypervisor().IPv4(GatewayVM)
	if ip == "" {
		return "", errors.New("passerelle sans adresse IP (encore en démarrage ?)")
	}
	return g.l.VMUser() + "@" + ip, nil
}

// SSH lance un script sur la passerelle ; sortie et erreur renvoyées ensemble.
func (g *Gateway) SSH(script string, stdin io.Reader) ([]byte, error) {
	t, err := g.target()
	if err != nil {
		return nil, err
	}
	stdout, stderr, err := g.l.shell(t, script, stdin)
	out := append(stdout, stderr...)
	if err != nil {
		return out, fmt.Errorf("%s : %w", strings.TrimSpace(string(out)), err)
	}
	return out, nil
}

// sshOut lance un script et ne renvoie que sa sortie standard (lecture d'état).
func (g *Gateway) sshOut(script string) ([]byte, error) {
	t, err := g.target()
	if err != nil {
		return nil, err
	}
	stdout, _, err := g.l.shell(t, script, nil)
	return stdout, err
}

// Interactive ouvre un shell (ou lance une commande) sur la passerelle, terminal compris.
func (g *Gateway) Interactive(args []string) error {
	t, err := g.target()
	if err != nil {
		return err
	}
	cmd := exec.Command("ssh", append(append(g.l.SSHArgs(false), t), args...)...) // interactif : pas de BatchMode
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return cmd.Run()
}

// gwctl lance « sudo gwctl ARGS » sur la passerelle.
func (g *Gateway) gwctl(args ...string) ([]byte, error) {
	return g.SSH("sudo gwctl "+strings.Join(args, " "), nil)
}

// ---------------------------------------------------------------- modules vus par la passerelle

// gwModule est un module avec un réseau, tel que la passerelle le voit.
type gwModule struct {
	*Module
	hosts map[string]int    // VMs à IP fixe + réservations
	macs  map[string]string // nom -> MAC
}

func (g *Gateway) module(name string) (*gwModule, error) {
	m, err := g.l.Module(name)
	if err != nil {
		return nil, err
	}
	if m.Env == nil {
		if m.YAML != nil {
			return nil, fmt.Errorf("le module %s n'a pas encore de réseau (labctl %s up)", name, name)
		}
		return nil, fmt.Errorf("le module %s n'a pas de réseau", name)
	}
	gm := &gwModule{Module: m, hosts: map[string]int{}, macs: map[string]string{}}
	for vm, o := range m.Env.Hosts {
		gm.hosts[vm], gm.macs[vm] = o, m.Env.Macs[vm]
	}
	for h, o := range m.Conf.Hosts {
		gm.hosts[h], gm.macs[h] = o, m.Conf.HostMACs[h]
	}
	return gm, nil
}

func (m *gwModule) mac() string { return coreconf.GatewayMAC(m.Env.Subnet) }

// hostIP renvoie l'adresse fixe d'une VM ou d'une réservation du module.
func (m *gwModule) hostIP(vm string) (string, error) {
	o, ok := m.hosts[vm]
	if !ok {
		names := make([]string, 0, len(m.hosts))
		for n := range m.hosts {
			names = append(names, n)
		}
		sort.Strings(names)
		return "", fmt.Errorf("VM sans IP fixe ou inconnue dans le module %s : %s (%s)", m.Name, vm, strings.Join(names, ", "))
	}
	return fmt.Sprintf("%s.%d", m.Env.Subnet, o), nil
}

// gwModules renvoie les modules qui ont un réseau, par nom.
func (g *Gateway) gwModules() ([]*gwModule, error) {
	mods, err := g.l.Modules()
	if err != nil {
		return nil, err
	}
	var out []*gwModule
	for _, m := range mods {
		if m.Env == nil {
			continue
		}
		gm, err := g.module(m.Name)
		if err != nil {
			return nil, err
		}
		out = append(out, gm)
	}
	return out, nil
}

// Attached indique si la passerelle a sa carte sur le réseau d'un module.
func (g *Gateway) attached(m *gwModule) bool {
	ifs, _ := g.l.Hypervisor().Interfaces(GatewayVM)
	return slices.ContainsFunc(ifs, func(i virt.Interface) bool { return strings.EqualFold(i.MAC, m.mac()) })
}

// ---------------------------------------------------------------- application de la configuration

// Apply génère la configuration (réseau .254, DHCP, DNS, liens, VMs bloquées) depuis les module.yaml,
// l'installe sur la passerelle et recharge ses services.
func (g *Gateway) Apply() error {
	if err := g.require(); err != nil {
		return err
	}
	in, err := g.l.CoreInput()
	if err != nil {
		return err
	}
	for _, s := range in.Skipped {
		g.logf("attention: %s", s)
	}
	files, err := coreconf.Render(in)
	if err != nil {
		return fmt.Errorf("génération de la configuration impossible : %w", err)
	}
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	dirs := map[string]bool{}
	for _, n := range names {
		if d := filepath.Dir(n); !dirs[d] {
			dirs[d] = true
			_ = tw.WriteHeader(&tar.Header{Name: d + "/", Typeflag: tar.TypeDir, Mode: 0o755, ModTime: time.Now()})
		}
		_ = tw.WriteHeader(&tar.Header{Name: n, Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(files[n])), ModTime: time.Now()})
		_, _ = tw.Write([]byte(files[n]))
	}
	if err := tw.Close(); err != nil {
		return err
	}
	if _, err := g.SSH(`set -e
t=$(mktemp -d); tar -C $t --no-same-owner -xf -
sudo rm -rf /etc/gwdns && sudo mv $t/gwdns /etc/gwdns && sudo chmod 755 /etc/gwdns
sudo rm -f /etc/systemd/network/50-gw-*.network /etc/systemd/network/60-modules.network
sudo cp $t/networkd/* /etc/systemd/network/ 2>/dev/null || true
rm -rf $t
sudo networkctl reload`, &buf); err != nil {
		return fmt.Errorf("installation de la configuration : %w", err)
	}

	// Adresses .254 en place avant de (re)lancer DHCP et DNS
	instances := []string{"dhcp"}
	byName := map[string]coreconf.Module{}
	for _, m := range in.Modules {
		byName[m.Name] = m
	}
	for _, name := range in.Attached {
		instances = append(instances, name+"-closed", name+"-open")
		addr := fmt.Sprintf("%s.%d/24", byName[name].Subnet, coreconf.GatewayOctet)
		ok := g.l.poll(30*time.Second, 2*time.Second, func() bool {
			out, err := g.sshOut("ip -4 -br addr")
			return err == nil && slices.Contains(strings.Fields(string(out)), addr)
		})
		if !ok {
			return fmt.Errorf("la passerelle n'a pas pris %s sur le réseau du module %s", addr, name)
		}
	}
	if _, err := g.SSH(fmt.Sprintf(`for u in $(systemctl list-units --all --plain --no-legend 'gwdns@*' | awk '{print $1}'); do
  i=${u#gwdns@}; i=${i%%.service}; [ -f /etc/gwdns/$i.conf ] || sudo systemctl disable -q --now $u; done
sudo systemctl stop gwdns@dhcp 2>/dev/null; sudo gwctl prune-leases
for i in %s; do sudo systemctl enable -q gwdns@$i && sudo systemctl restart gwdns@$i; done
sudo gwctl apply
# Portails captifs : configuration relue, sessions remises dans les ensembles recréés par gwctl apply
if systemctl cat gwportal >/dev/null 2>&1 && [ -x /usr/local/sbin/labctl ]; then sudo systemctl restart gwportal; fi`, strings.Join(instances, " ")), nil); err != nil {
		return fmt.Errorf("redémarrage des services de la passerelle : %w", err)
	}
	if err := g.revokeVulnerable(); err != nil {
		return fmt.Errorf("fermeture d'Internet des modules vulnérables : %w", err)
	}
	if err := g.ensureProxy(files); err != nil {
		return fmt.Errorf("proxy : %w", err)
	}
	attached := strings.Join(in.Attached, " ")
	if attached == "" {
		attached = "aucun module branché"
	}
	g.logf("ok  passerelle à jour : %s (DHCP + %d instances DNS)", attached, len(instances)-1)
	return nil
}

// applyIfRunning applique la configuration si la passerelle tourne.
func (g *Gateway) applyIfRunning() error {
	if !g.Running() {
		return nil
	}
	return g.Apply()
}
