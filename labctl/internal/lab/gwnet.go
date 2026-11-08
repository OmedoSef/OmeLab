package lab

import (
	"fmt"
	"net/netip"
	"slices"
	"sort"
	"strings"
	"text/tabwriter"

	"labs/labctl/internal/coreconf"
	"labs/labctl/internal/virt"
)

// Passerelle core-gw : branchement sur les réseaux des modules, accès à Internet, liens entre modules
// (voir gateway.go).

// ---------------------------------------------------------------- branchement

// Attach branche la passerelle sur le réseau d'un module (apply : puis applique la configuration).
func (g *Gateway) Attach(name string, apply bool) error {
	m, err := g.module(name)
	if err != nil {
		return err
	}
	v := g.l.Hypervisor()
	if g.l.DomState(GatewayVM) == virt.StateAbsent {
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
	if g.l.DomState(GatewayVM) == virt.StateAbsent || !g.attached(m) {
		g.logf("ok  passerelle non branchée sur %s", m.Env.Net)
		return nil
	}
	if g.Running() {
		if err := g.InternetOff(name, nil); err != nil {
			return err
		}
	}
	if err := g.l.Hypervisor().DetachInterface(GatewayVM, m.mac(), g.Running()); err != nil {
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
	if m.Vulnerable() {
		return fmt.Errorf("%s est un module vulnérable (vulnerable: true) : Internet n'y est jamais ouvert à la main", name)
	}
	return g.internetOn(m, vms)
}

// openForInstall ouvre Internet à une VM le temps de son installation (internet: install), y compris dans un
// module vulnérable : c'est la seule ouverture qui y soit permise, refermée dès la VM prête.
func (g *Gateway) openForInstall(name, vm string) error {
	m, err := g.module(name)
	if err != nil {
		return err
	}
	return g.internetOn(m, []string{vm})
}

func (g *Gateway) internetOn(m *gwModule, vms []string) error {
	name := m.Name
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

// revokeVulnerable retire des autorisations Internet de la passerelle toute adresse d'un module vulnérable. Une
// ouverture d'installation (internet: install) que labctl n'a pas pu refermer — interrompu, tué, plantage — ou un
// module passé vulnérable après coup ne survivent donc pas à l'application suivante de la configuration.
func (g *Gateway) revokeVulnerable() error {
	mods, err := g.gwModules()
	if err != nil {
		return err
	}
	var red []netip.Prefix
	for _, m := range mods {
		if p, err := netip.ParsePrefix(m.Env.Subnet + ".0/24"); err == nil && m.Vulnerable() {
			red = append(red, p)
		}
	}
	if len(red) == 0 {
		return nil
	}
	out, err := g.gwctl("status")
	if err != nil {
		return err
	}
	for _, x := range vulnerableGrants(string(out), red) {
		if _, err := g.gwctl("revoke", x); err != nil {
			return err
		}
		g.logf("attention: Internet refermé pour %s (module vulnérable : une ouverture d'installation était restée)", x)
	}
	return nil
}

// vulnerableGrants renvoie, dans la sortie de « gwctl status », les autorisations (allowed) qui touchent un des
// réseaux red.
func vulnerableGrants(status string, red []netip.Prefix) []string {
	var out []string
	for _, line := range strings.Split(status, "\n") {
		kind, x, ok := strings.Cut(strings.TrimSpace(line), " ")
		if !ok || kind != "allowed" {
			continue
		}
		p, err := netip.ParsePrefix(x)
		if err != nil {
			a, err := netip.ParseAddr(x)
			if err != nil {
				continue
			}
			p = netip.PrefixFrom(a, a.BitLen())
		}
		if slices.ContainsFunc(red, p.Overlaps) {
			out = append(out, x)
		}
	}
	return out
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
