package lab

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"text/tabwriter"

	"labs/labctl/internal/coreconf"
	"labs/labctl/internal/virt"
)

// Passerelle core-gw : réservations DHCP, baux et DNS (voir gateway.go).

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
	if st.State != virt.StateRunning {
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
