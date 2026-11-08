package coreconf

import (
	"fmt"
	"net/netip"
	"strings"

	"labs/labctl/internal/module"
	"labs/labctl/internal/names"
)

// Contrôle des identifiants recopiés tels quels dans la configuration de la passerelle (nftables, dnsmasq,
// systemd-networkd ; le nom du module sert aussi de nom de fichier). Un saut de ligne, une accolade, un guillemet
// ou un « / » y injecteraient des règles ou des directives exécutées en root sur core-gw. Ce contrôle ne dépend
// d'aucune validation faite ailleurs : un module.yaml jamais validé (déposé dans modules/, jamais lancé) ne
// peut rien injecter.

// Check vérifie qu'un module ne porte que des identifiants sûrs pour la passerelle.
func (m *Module) Check() error {
	if !names.Label(m.Name) {
		return fmt.Errorf("nom de module invalide %q", m.Name)
	}
	if a, err := netip.ParseAddr(m.Subnet + ".0"); err != nil || !a.Is4() || strings.Count(m.Subnet, ".") != 2 {
		return fmt.Errorf("sous-réseau invalide %q", m.Subnet)
	}
	if !names.Domain(m.Domain) {
		return fmt.Errorf("domaine invalide %q", m.Domain)
	}
	for vm, octet := range m.Hosts {
		if !names.Label(vm) {
			return fmt.Errorf("nom de VM ou de réservation invalide %q", vm)
		}
		if octet < 1 || octet > 254 {
			return fmt.Errorf("%s : adresse .%d hors de 1-254", vm, octet)
		}
	}
	for vm, mac := range m.MACs {
		if !names.Label(vm) || !names.MAC(strings.ToLower(mac)) {
			return fmt.Errorf("MAC invalide pour %q : %q", vm, mac)
		}
	}
	for vm := range m.NoRoute {
		if !names.Label(vm) {
			return fmt.Errorf("nom de VM branchée invalide %q", vm)
		}
	}
	for a, target := range m.Aliases {
		if !names.Label(a) {
			return fmt.Errorf("alias invalide %q", a)
		}
		if !names.Label(target) && !names.Domain(target) {
			return fmt.Errorf("cible d'alias invalide %q", target)
		}
	}
	for _, l := range append(append([]string{}, m.Links...), m.Sees...) {
		if !names.Label(l) {
			return fmt.Errorf("module lié invalide %q", l)
		}
	}
	for _, cn := range m.Conns {
		if cn.To != module.Internet {
			mod, vm := cn.Target()
			if !names.Label(mod) || (vm != "" && !names.Label(vm)) || strings.Count(cn.To, "/") > 1 {
				return fmt.Errorf("cible de connexion invalide %q", cn.To)
			}
		}
		for _, f := range cn.From {
			if !names.Label(f) {
				return fmt.Errorf("source de connexion invalide %q", f)
			}
		}
	}
	return nil
}

// Check vérifie l'entrée complète : chaque module, le domaine racine et le DNS amont.
func (in *Input) Check() error {
	if !names.Domain(in.RootDomain) {
		return fmt.Errorf("domaine racine invalide %q", in.RootDomain)
	}
	if in.Upstream != "" {
		if _, err := netip.ParseAddr(in.Upstream); err != nil {
			return fmt.Errorf("DNS amont invalide %q", in.Upstream)
		}
	}
	for i := range in.Modules {
		if err := in.Modules[i].Check(); err != nil {
			return fmt.Errorf("module %s : %w", in.Modules[i].Name, err)
		}
	}
	return nil
}
