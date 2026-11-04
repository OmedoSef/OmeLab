package lab

import (
	"regexp"
	"strings"

	"labs/labctl/internal/coreconf"
)

var reNetIP = regexp.MustCompile(`<ip address='([^']+)'`)

// CoreInput rassemble ce qu'il faut pour générer la configuration de la passerelle : les modules
// (module.env + core.conf), ceux sur lesquels la passerelle est branchée, et le DNS amont.
func (l *Lab) CoreInput() (coreconf.Input, error) {
	in := coreconf.Input{RootDomain: l.pkiDomain()}
	mods, err := l.Modules()
	if err != nil {
		return in, err
	}
	gwMACs := ""
	if out, err := virsh("domiflist", GatewayVM); err == nil {
		gwMACs = strings.ToLower(out)
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
		if strings.Contains(gwMACs, coreconf.GatewayMAC(m.Env.Subnet)) {
			in.Attached = append(in.Attached, m.Name)
		}
	}
	if out, err := virsh("net-dumpxml", "default"); err == nil {
		if mm := reNetIP.FindStringSubmatch(out); mm != nil {
			in.Upstream = mm[1]
		}
	}
	return in, nil
}
