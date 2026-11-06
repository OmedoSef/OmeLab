package lab

import (
	"labs/labctl/internal/coreconf"
	"labs/labctl/internal/virt"
	"slices"
	"strings"
)

// CoreInput rassemble ce qu'il faut pour générer la configuration de la passerelle : les modules
// (module.env + core.conf), ceux sur lesquels la passerelle est branchée, et le DNS amont.
func (l *Lab) CoreInput() (coreconf.Input, error) {
	in := coreconf.Input{RootDomain: l.pkiDomain()}
	mods, err := l.Modules()
	if err != nil {
		return in, err
	}
	gwMACs := map[string]bool{}
	if ifs, err := virt.Shared().Interfaces(GatewayVM); err == nil {
		for _, i := range ifs {
			gwMACs[i.MAC] = true
		}
	}
	for _, m := range mods {
		if m.Env == nil {
			continue
		}
		cm := coreconf.Module{
			Name: m.Name, Subnet: m.Env.Subnet, Domain: m.Env.Domain,
			Hosts: map[string]int{}, MACs: map[string]string{},
			NoInternet: m.Env.NoInternet, Aliases: m.Conf.Aliases, Links: m.Conf.Links,
		}
		if m.YAML != nil {
			cm.Conns = m.YAML.Connections
		}
		for h := range m.Conf.Attached {
			if cm.NoRoute == nil {
				cm.NoRoute = map[string]bool{}
			}
			cm.NoRoute[h] = true
		}
		for vm, o := range m.Env.Hosts {
			cm.Hosts[vm] = o
			if mac := m.Env.Macs[vm]; mac != "" {
				cm.MACs[vm] = mac
			}
		}
		// Réservations de core.conf (comme load_module côté bash : elles complètent module.env)
		for h, o := range m.Conf.Hosts {
			cm.Hosts[h] = o
			if mac := m.Conf.HostMACs[h]; mac != "" {
				cm.MACs[h] = mac
			}
		}
		in.Modules = append(in.Modules, cm)
		if gwMACs[coreconf.GatewayMAC(m.Env.Subnet)] {
			in.Attached = append(in.Attached, m.Name)
		}
	}
	// Un module dont une VM est branchée ailleurs voit les noms de ce module-là (DNS de sa carte principale)
	byName := map[string]int{}
	for i, m := range in.Modules {
		byName[m.Name] = i
	}
	for _, m := range mods {
		if m.Conf == nil {
			continue
		}
		for _, ref := range m.Conf.Attached {
			owner, _, _ := strings.Cut(ref, "/")
			if i, ok := byName[owner]; ok && !slices.Contains(in.Modules[i].Sees, m.Name) {
				in.Modules[i].Sees = append(in.Modules[i].Sees, m.Name)
			}
		}
	}
	// DNS amont des instances open : l'hôte sur le réseau NAT libvirt « default »
	if nets, err := virt.Shared().Networks(); err == nil {
		for _, n := range nets {
			if n.Name == "default" {
				in.Upstream = n.HostIP
			}
		}
	}
	return in, nil
}
