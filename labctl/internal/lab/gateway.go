package lab

import (
	"archive/tar"
	"bytes"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"labs/labctl/internal/coreconf"
	"labs/labctl/internal/portal"
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
func (g *Gateway) Running() bool { return DomState(GatewayVM) == "running" }

func (g *Gateway) require() error {
	switch DomState(GatewayVM) {
	case "running":
		return nil
	case "absente":
		return errors.New("passerelle absente : labctl core gw")
	}
	return errors.New("passerelle arrêtée : labctl core start")
}

// target renvoie utilisateur@IP de la passerelle (sur le réseau NAT libvirt).
func (g *Gateway) target() (string, error) {
	ip := virt.Shared().IPv4(GatewayVM)
	if ip == "" {
		return "", errors.New("passerelle sans adresse IP (encore en démarrage ?)")
	}
	return g.l.Setting("LABS_VM_USER", "lab") + "@" + ip, nil
}

// SSH lance un script sur la passerelle ; sortie et erreur renvoyées ensemble.
func (g *Gateway) SSH(script string, stdin io.Reader) ([]byte, error) {
	t, err := g.target()
	if err != nil {
		return nil, err
	}
	cmd := exec.Command("ssh", append(append([]string{}, SSHOpts...), "-o", "BatchMode=yes", t, script)...)
	cmd.Stdin = stdin
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err = cmd.Run()
	if err != nil {
		return out.Bytes(), fmt.Errorf("%s : %w", strings.TrimSpace(out.String()), err)
	}
	return out.Bytes(), nil
}

// sshOut lance un script et ne renvoie que sa sortie standard (lecture d'état).
func (g *Gateway) sshOut(script string) ([]byte, error) {
	t, err := g.target()
	if err != nil {
		return nil, err
	}
	cmd := exec.Command("ssh", append(append([]string{}, SSHOpts...), "-o", "BatchMode=yes", t, script)...)
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	return cmd.Output()
}

// Interactive ouvre un shell (ou lance une commande) sur la passerelle, terminal compris.
func (g *Gateway) Interactive(args []string) error {
	t, err := g.target()
	if err != nil {
		return err
	}
	cmd := exec.Command("ssh", append(append(append([]string{}, SSHOpts...), t), args...)...)
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
	ifs, _ := virt.Shared().Interfaces(GatewayVM)
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
		ok := false
		for t := 0; t < 30; t += 2 {
			if out, err := g.sshOut("ip -4 -br addr"); err == nil && slices.Contains(strings.Fields(string(out)), addr) {
				ok = true
				break
			}
			time.Sleep(2 * time.Second)
		}
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

// ---------------------------------------------------------------- branchement

// Attach branche la passerelle sur le réseau d'un module (apply : puis applique la configuration).
func (g *Gateway) Attach(name string, apply bool) error {
	m, err := g.module(name)
	if err != nil {
		return err
	}
	v := virt.Shared()
	if DomState(GatewayVM) == "absente" {
		g.logf("pas de passerelle : labctl core gw (DHCP, DNS et Internet du module %s)", name)
		return nil
	}
	if !v.NetExists(m.Env.Net) {
		return fmt.Errorf("réseau %s absent (labctl %s up)", m.Env.Net, name)
	}
	if g.attached(m) {
		g.logf("ok  passerelle déjà branchée sur %s", m.Env.Net)
	} else {
		if err := v.AttachInterface(GatewayVM, m.Env.Net, m.mac(), "virtio", g.Running()); err != nil {
			return fmt.Errorf("branchement sur %s : %w", m.Env.Net, err)
		}
		g.logf("ok  passerelle branchée sur %s", m.Env.Net)
	}
	if apply {
		return g.applyIfRunning()
	}
	return nil
}

// Detach débranche la passerelle du réseau d'un module (Internet du module refermé avant).
func (g *Gateway) Detach(name string) error {
	m, err := g.module(name)
	if err != nil {
		return err
	}
	if DomState(GatewayVM) == "absente" || !g.attached(m) {
		g.logf("ok  passerelle non branchée sur %s", m.Env.Net)
		return nil
	}
	if g.Running() {
		if err := g.InternetOff(name, nil); err != nil {
			return err
		}
	}
	if err := virt.Shared().DetachInterface(GatewayVM, m.mac(), g.Running()); err != nil {
		return fmt.Errorf("débranchement de %s : %w", m.Env.Net, err)
	}
	g.logf("ok  passerelle débranchée de %s (le module n'a plus de DHCP, DNS ni Internet)", m.Env.Net)
	return g.applyIfRunning()
}

// ---------------------------------------------------------------- Internet

// InternetOn ouvre Internet au module entier, ou seulement à des VMs.
func (g *Gateway) InternetOn(name string, vms []string) error {
	m, err := g.module(name)
	if err != nil {
		return err
	}
	if err := g.require(); err != nil {
		return err
	}
	out, err := g.sshOut("ip -4 -br addr")
	if err != nil || !slices.Contains(strings.Fields(string(out)), fmt.Sprintf("%s.%d/24", m.Env.Subnet, coreconf.GatewayOctet)) {
		return fmt.Errorf("passerelle non branchée sur %s (labctl core attach %s)", m.Env.Net, name)
	}
	if len(vms) == 0 {
		if _, err := g.gwctl("allow", m.Env.Subnet+".0/24"); err != nil {
			return err
		}
	}
	for _, vm := range vms {
		if slices.Contains(m.Env.NoInternet, vm) {
			return fmt.Errorf("%s est « internet: never » dans le module %s : jamais d'accès Internet", vm, name)
		}
		ip, err := m.hostIP(vm)
		if err != nil {
			return err
		}
		if _, err := g.gwctl("allow", ip); err != nil {
			return err
		}
	}
	msg := "ok  Internet ouvert pour " + name
	if len(vms) > 0 {
		msg += " (" + strings.Join(vms, " ") + ")"
	}
	if len(m.Env.NoInternet) > 0 {
		msg += " ; toujours bloquées : " + strings.Join(m.Env.NoInternet, " ")
	}
	g.logf("%s", msg)
	return nil
}

// InternetOff referme Internet au module entier (toutes ses autorisations), ou à des VMs.
func (g *Gateway) InternetOff(name string, vms []string) error {
	m, err := g.module(name)
	if err != nil {
		return err
	}
	if err := g.require(); err != nil {
		return err
	}
	var targets []string
	if len(vms) == 0 {
		targets = append(targets, m.Env.Subnet+".0/24")
		for vm := range m.hosts {
			ip, _ := m.hostIP(vm)
			targets = append(targets, ip)
		}
	}
	for _, vm := range vms {
		ip, err := m.hostIP(vm)
		if err != nil {
			return err
		}
		targets = append(targets, ip)
	}
	sort.Strings(targets)
	script := ""
	for _, t := range targets {
		script += "sudo gwctl revoke " + t + " && "
	}
	if _, err := g.SSH(script+"true", nil); err != nil {
		return err
	}
	msg := "ok  Internet fermé pour " + name
	if len(vms) > 0 {
		msg += " (" + strings.Join(vms, " ") + ")"
	}
	g.logf("%s", msg)
	return nil
}

// InternetState est l'état Internet d'un module : "non branché", "fermé", ou les VMs ouvertes.
type InternetState struct {
	Module   string
	Net      string
	Attached bool
	All      bool     // tout le module
	VMs      []string // VMs ouvertes
	Never    []string
}

func (s InternetState) String() string {
	switch {
	case !s.Attached:
		return "non branché (labctl core attach " + s.Module + ")"
	case !s.All && len(s.VMs) == 0:
		return "fermé"
	}
	open := strings.Join(s.VMs, " ")
	if s.All {
		open = "tout le module"
	}
	out := "OUVERT : " + open
	if len(s.Never) > 0 {
		out += " (jamais : " + strings.Join(s.Never, " ") + ")"
	}
	return out
}

// Internet renvoie l'état Internet de chaque module (passerelle démarrée).
func (g *Gateway) Internet() ([]InternetState, error) {
	if err := g.require(); err != nil {
		return nil, err
	}
	out, err := g.sshOut("sudo gwctl status")
	if err != nil {
		return nil, fmt.Errorf("passerelle injoignable : %w", err)
	}
	var allowed []string
	for _, line := range strings.Split(string(out), "\n") {
		if f := strings.Fields(line); len(f) == 2 && f[0] == "allowed" {
			allowed = append(allowed, f[1])
		}
	}
	mods, err := g.gwModules()
	if err != nil {
		return nil, err
	}
	var res []InternetState
	for _, m := range mods {
		s := InternetState{Module: m.Name, Net: m.Env.Net, Attached: g.attached(m), Never: m.Env.NoInternet}
		for _, a := range allowed {
			if !strings.HasPrefix(a, m.Env.Subnet+".") {
				continue
			}
			if strings.HasSuffix(a, "/24") {
				s.All, s.VMs = true, nil
				break
			}
			for vm := range m.hosts {
				if ip, _ := m.hostIP(vm); ip == a {
					s.VMs = append(s.VMs, vm)
				}
			}
		}
		sort.Strings(s.VMs)
		res = append(res, s)
	}
	return res, nil
}

// PrintInternet affiche l'état Internet des modules.
func (g *Gateway) PrintInternet() error {
	if !g.Running() {
		g.logf("attention: passerelle arrêtée ou absente : ni Internet, ni DHCP, ni DNS pour les modules")
		return nil
	}
	st, err := g.Internet()
	if err != nil {
		return err
	}
	tw := tabwriter.NewWriter(g.Out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "MODULE\tRÉSEAU\tINTERNET")
	for _, s := range st {
		fmt.Fprintf(tw, "%s\t%s\t%s\n", s.Module, s.Net, s)
	}
	return tw.Flush()
}

// ---------------------------------------------------------------- liens

// Link autorise a à joindre et résoudre b (both : et inversement), puis applique.
func (g *Gateway) Link(a, b string, on, both bool) error {
	pairs := [][2]string{{a, b}}
	if both {
		pairs = append(pairs, [2]string{b, a})
	}
	for _, p := range pairs {
		if err := g.l.ConfLink(p[0], p[1], on); err != nil {
			return err
		}
		if on {
			g.logf("ok  lien %s -> %s : %s joint et résout %s", p[0], p[1], p[0], p[1])
		} else {
			g.logf("ok  lien %s -> %s retiré", p[0], p[1])
		}
	}
	return g.applyIfRunning()
}

// PrintLinks affiche les liens entre modules.
func (g *Gateway) PrintLinks() error {
	mods, err := g.gwModules()
	if err != nil {
		return err
	}
	tw := tabwriter.NewWriter(g.Out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "MODULE\tPEUT JOINDRE")
	any := false
	for _, m := range mods {
		dst := strings.Join(m.Conf.Links, " ")
		if dst == "" {
			dst = "(aucun module : isolé)"
		} else {
			any = true
		}
		fmt.Fprintf(tw, "%s\t%s\n", m.Name, dst)
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	if !any {
		g.logf("aucun lien : chaque module est isolé des autres (labctl core link A B)")
	}
	return nil
}

// ---------------------------------------------------------------- DHCP et DNS

func (g *Gateway) selectModules(names []string) ([]*gwModule, error) {
	if len(names) == 0 {
		return g.gwModules()
	}
	var out []*gwModule
	for _, n := range names {
		m, err := g.module(n)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, nil
}

func (m *gwModule) hostsByIP() []string {
	names := make([]string, 0, len(m.hosts))
	for n := range m.hosts {
		names = append(names, n)
	}
	sort.Slice(names, func(i, j int) bool {
		if m.hosts[names[i]] != m.hosts[names[j]] {
			return m.hosts[names[i]] < m.hosts[names[j]]
		}
		return names[i] < names[j]
	})
	return names
}

// PrintReservations affiche les IP fixes des modules.
func (g *Gateway) PrintReservations(names []string) error {
	mods, err := g.selectModules(names)
	if err != nil {
		return err
	}
	for _, m := range mods {
		g.logf("==> Module %s : %s.0/24, DHCP libre .%d-.%d, passerelle/DNS .%d", m.Name, m.Env.Subnet,
			coreconf.DHCPRangeStart, coreconf.DHCPRangeEnd, coreconf.GatewayOctet)
		tw := tabwriter.NewWriter(g.Out, 0, 0, 2, ' ', 0)
		for _, h := range m.hostsByIP() {
			ip, _ := m.hostIP(h)
			mac := m.macs[h]
			if mac == "" {
				mac = "(pas de MAC : DNS seulement)"
			}
			fmt.Fprintf(tw, "  %s\t%s\t%s\n", ip, h, mac)
		}
		tw.Flush()
	}
	return nil
}

// PrintLeases affiche les baux DHCP en cours (d'un module, ou de tous).
func (g *Gateway) PrintLeases(module string) error {
	st := g.l.Core()
	if st.Error != "" {
		return errors.New(st.Error)
	}
	if st.State != "running" {
		return g.require()
	}
	prefix := ""
	if module != "" {
		m, err := g.module(module)
		if err != nil {
			return err
		}
		prefix = m.Env.Subnet + "."
	}
	tw := tabwriter.NewWriter(g.Out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "EXPIRATION\tMAC\tIP\tNOM\tMODULE")
	for _, le := range st.Leases {
		if strings.HasPrefix(le.IP, prefix) {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", le.Expires, le.MAC, le.IP, le.Name, le.Module)
		}
	}
	return tw.Flush()
}

// AddReservation réserve <sous-réseau>.octet pour mac, sous le nom name, puis applique.
func (g *Gateway) AddReservation(module, name string, octet int, mac string) error {
	if err := g.l.ConfAddHost(module, name, octet, mac); err != nil {
		return err
	}
	m, _ := g.module(module)
	g.logf("ok  réservation %s : %s.%d pour %s (DNS : %s.%s)", name, m.Env.Subnet, octet, strings.ToLower(mac), name, m.Env.Domain)
	return g.applyIfRunning()
}

// DelReservation retire une réservation, puis applique.
func (g *Gateway) DelReservation(module, name string) error {
	if err := g.l.ConfDelHost(module, name); err != nil {
		return err
	}
	g.logf("ok  réservation %s retirée", name)
	return g.applyIfRunning()
}

// Release libère des baux DHCP (IP et nom DNS disparaissent tout de suite).
func (g *Gateway) Release(macs []string) error {
	if err := g.require(); err != nil {
		return err
	}
	for _, mac := range macs {
		if !reMAC.MatchString(strings.ToLower(mac)) {
			return fmt.Errorf("MAC invalide : %s", mac)
		}
	}
	out, err := g.gwctl(append([]string{"release"}, macs...)...)
	fmt.Fprint(g.Out, string(out))
	return err
}

// PrintDNS affiche les noms, alias et modules résolus.
func (g *Gateway) PrintDNS(names []string) error {
	mods, err := g.selectModules(names)
	if err != nil {
		return err
	}
	for _, m := range mods {
		g.logf("==> Module %s : domaine %s, DNS %s.%d", m.Name, m.Env.Domain, m.Env.Subnet, coreconf.GatewayOctet)
		tw := tabwriter.NewWriter(g.Out, 0, 0, 2, ' ', 0)
		hosts := make([]string, 0, len(m.hosts))
		for h := range m.hosts {
			hosts = append(hosts, h)
		}
		sort.Strings(hosts)
		for _, h := range hosts {
			ip, _ := m.hostIP(h)
			fmt.Fprintf(tw, "  %s.%s\t%s\n", h, m.Env.Domain, ip)
		}
		aliases := make([]string, 0, len(m.Conf.Aliases))
		for a := range m.Conf.Aliases {
			aliases = append(aliases, a)
		}
		sort.Strings(aliases)
		for _, a := range aliases {
			fmt.Fprintf(tw, "  %s.%s\t-> %s\n", a, m.Env.Domain, m.Conf.Aliases[a])
		}
		tw.Flush()
		links := strings.Join(m.Conf.Links, " ")
		if links == "" {
			links = "(aucun autre module)"
		}
		g.logf("  résout aussi : %s", links)
	}
	return nil
}

// SetAlias crée (target non vide) ou retire un alias DNS, puis applique.
func (g *Gateway) SetAlias(module, name, target string) error {
	m, err := g.module(module)
	if err != nil {
		return err
	}
	if target != "" {
		if err := g.l.ConfAddAlias(module, name, target); err != nil {
			return err
		}
		g.logf("ok  alias %s.%s -> %s", name, m.Env.Domain, target)
	} else {
		if err := g.l.ConfDelAlias(module, name); err != nil {
			return err
		}
		g.logf("ok  alias %s.%s retiré", name, m.Env.Domain)
	}
	return g.applyIfRunning()
}

// ---------------------------------------------------------------- cycle de vie de la passerelle

// Create crée la passerelle si besoin (Debian, nftables, dnsmasq, fichiers système), la démarre, la
// branche sur chaque module qui a un réseau, puis applique la configuration.
func (g *Gateway) Create() error {
	v := virt.Shared()
	if DomState(GatewayVM) != "absente" {
		g.logf("ok  %s existe déjà", GatewayVM)
	} else {
		tmp, err := os.MkdirTemp("", "labctl-gw-")
		if err != nil {
			return err
		}
		defer os.RemoveAll(tmp)
		s := VMSpec{
			Name: GatewayVM, Image: "debian13", CPUs: 1, MemoryMiB: 512, DiskGiB: 10,
			Nets: []NetSpec{{Network: "default"}}, Lab: "core", Role: "gateway",
			Packages: []string{"nftables", "dnsmasq", "dnsmasq-utils"},
			Run: []string{"sysctl --system >/dev/null", "systemctl disable --now dnsmasq",
				"systemctl daemon-reload && systemctl enable --now gw.service", "systemctl enable systemd-networkd"},
		}
		for _, f := range gwInstall {
			b, err := g.l.GatewayFile(f.name)
			if err != nil {
				return err
			}
			src := filepath.Join(tmp, f.name)
			if err := os.WriteFile(src, b, 0o600); err != nil {
				return err
			}
			s.Files = append(s.Files, FileSpec{Src: src, Dest: f.dest, Mode: f.mode})
		}
		if err := g.l.CreateVM(s, CreateOptions{Wait: true, Out: g.Out}); err != nil {
			return err
		}
	}
	if !g.Running() {
		if err := v.StartDomain(GatewayVM); err != nil {
			return err
		}
		g.waitSSH()
	}
	if err := g.installLabctl(); err != nil {
		return err
	}
	mods, err := g.gwModules()
	if err != nil {
		return err
	}
	for _, m := range mods {
		if !v.NetExists(m.Env.Net) {
			g.logf("module %s : réseau %s absent, branchement plus tard", m.Name, m.Env.Net)
			continue
		}
		if err := g.Attach(m.Name, false); err != nil {
			return err
		}
	}
	return g.Apply()
}

// waitSSH attend que la passerelle réponde en SSH (2 min au plus).
func (g *Gateway) waitSSH() bool {
	for t := 0; t < 120; t += 3 {
		if _, err := g.sshOut("true"); err == nil {
			return true
		}
		time.Sleep(3 * time.Second)
	}
	return false
}

// installLabctl copie labctl (binaire statique) sur la passerelle et active le service des portails captifs.
func (g *Gateway) installLabctl() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	f, err := os.Open(exe)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := g.SSH("sudo install -m 0755 /dev/stdin /usr/local/sbin/labctl", f); err != nil {
		return fmt.Errorf("copie de labctl sur la passerelle : %w", err)
	}
	if _, err := g.SSH("sudo systemctl daemon-reload && sudo systemctl enable -q gwportal", nil); err != nil {
		return err
	}
	g.logf("ok  labctl copié sur la passerelle (portails captifs)")
	return nil
}

// PortalSessions renvoie les sessions des portails captifs (vide si la passerelle n'en a pas).
func (g *Gateway) PortalSessions() ([]portal.Session, error) {
	out, err := g.sshOut("[ -x /usr/local/sbin/labctl ] && /usr/local/sbin/labctl gw-portal sessions --json || echo '[]'")
	if err != nil {
		return nil, err
	}
	var list []portal.Session
	return list, json.Unmarshal(out, &list)
}

// PortalKick ferme la session de ip sur le portail id.
func (g *Gateway) PortalKick(id int, ip string) error {
	if _, err := netip.ParseAddr(ip); err != nil {
		return fmt.Errorf("adresse invalide : %q", ip)
	}
	_, err := g.SSH(fmt.Sprintf("/usr/local/sbin/labctl gw-portal kick %d %s", id, ip), nil)
	return err
}

// Update pousse la version courante des fichiers système (embarqués ou remplacés), puis applique.
func (g *Gateway) Update() error {
	if err := g.require(); err != nil {
		return err
	}
	if _, err := g.SSH("dpkg -s dnsmasq dnsmasq-utils >/dev/null 2>&1 || sudo DEBIAN_FRONTEND=noninteractive apt-get install -qq -y dnsmasq dnsmasq-utils >/dev/null", nil); err != nil {
		return err
	}
	for _, f := range gwInstall {
		b, err := g.l.GatewayFile(f.name)
		if err != nil {
			return err
		}
		if _, err := g.SSH(fmt.Sprintf("sudo install -m %s /dev/stdin %s", f.mode, f.dest), bytes.NewReader(b)); err != nil {
			return fmt.Errorf("%s : %w", f.dest, err)
		}
	}
	// Le service dnsmasq de Debian n'est pas utilisé : instances gwdns@ (dhcp, <module>-open/closed)
	if _, err := g.SSH("sudo systemctl disable -q --now dnsmasq; sudo systemctl daemon-reload && sudo /usr/sbin/sysctl -q --system", nil); err != nil {
		return err
	}
	if err := g.installLabctl(); err != nil {
		return err
	}
	g.logf("ok  fichiers système de la passerelle à jour")
	return g.Apply()
}

// Start démarre la passerelle (Internet fermé pour tous les modules).
func (g *Gateway) Start() error {
	switch DomState(GatewayVM) {
	case "absente":
		return errors.New("passerelle absente : labctl core gw")
	case "running":
	default:
		if err := virt.Shared().StartDomain(GatewayVM); err != nil {
			return err
		}
	}
	g.logf("ok  %s démarrée (Internet fermé pour tous les modules)", GatewayVM)
	return nil
}

// Stop arrête la passerelle (ACPI).
func (g *Gateway) Stop() error {
	if !g.Running() {
		g.logf("ok  %s déjà arrêtée", GatewayVM)
		return nil
	}
	if err := virt.Shared().ShutdownDomain(GatewayVM); err != nil {
		return err
	}
	g.logf("ok  %s en cours d'arrêt : les modules n'ont plus de DHCP, DNS ni Internet", GatewayVM)
	return nil
}

// Destroy supprime la passerelle.
func (g *Gateway) Destroy() error {
	return g.l.DestroyVM(GatewayVM, false, g.Out)
}

// PrintStatus affiche la passerelle, ses cartes et services, l'état Internet et les liens.
func (g *Gateway) PrintStatus() error {
	st := g.l.Core()
	if st.State == "absente" {
		g.logf("attention: passerelle absente (labctl core gw)")
		return nil
	}
	wan := st.WAN
	if wan == "" {
		wan = "?"
	}
	g.logf("==> Passerelle %s : %s, WAN %s", GatewayVM, st.State, wan)
	if ifs, err := virt.Shared().Interfaces(GatewayVM); err == nil {
		for _, i := range ifs {
			g.logf("  carte %-18s sur %s", i.MAC, i.Network)
		}
	}
	if st.Error != "" {
		g.logf("attention: %s", st.Error)
	}
	for _, s := range st.Services {
		if strings.HasPrefix(s.Name, "gwdns@") {
			g.logf("  service %-28s %s", s.Name+".service", s.Sub)
		}
	}
	fmt.Fprintln(g.Out)
	if err := g.PrintInternet(); err != nil {
		return err
	}
	fmt.Fprintln(g.Out)
	return g.PrintLinks()
}

// internetText renvoie l'état Internet par module, au format de « core internet status » (GUI).
func (g *Gateway) internetText() map[string]string {
	out := map[string]string{}
	if !g.Running() {
		return out
	}
	st, err := g.Internet()
	if err != nil {
		return out
	}
	for _, s := range st {
		out[s.Module] = s.String()
	}
	return out
}
