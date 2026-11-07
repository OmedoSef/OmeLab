package lab

import (
	"fmt"
	"regexp"
	"strings"

	"labs/labctl/internal/coreconf"
	mod "labs/labctl/internal/module"
)

// Édition de la section réseau d'un module.yaml : réservations DHCP (dns.hosts), alias DNS (dns.aliases), liens (dns.links).
// Mêmes règles que les commandes « labctl core dhcp|dns|link » ; rien n'est appliqué sur la
// passerelle (voir PendingCoreChanges et « core apply »).

var (
	reHostName = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)
	reMAC      = regexp.MustCompile(`^([0-9a-f]{2}:){5}[0-9a-f]{2}$`)
)

// confModule renvoie un module qui a un réseau.
func (l *Lab) confModule(name string) (*Module, error) {
	m, err := l.Module(name)
	if err != nil {
		return nil, err
	}
	if m.Env == nil {
		return nil, fmt.Errorf("le module %s n'a pas encore de réseau (labctl %s up)", name, name)
	}
	return m, nil
}

// saveEdit enregistre une édition dans le module.yaml (commentaires préservés).
func saveEdit(m *Module, yamlEdit func(*mod.Module)) error {
	if m.HasYAML {
		y, err := mod.Load(m.Dir)
		if err != nil {
			return fmt.Errorf("%s illisible : %w", mod.FileName, err)
		}
		yamlEdit(y)
		return y.Save()
	}
	return fmt.Errorf("module %s sans %s", m.Name, mod.FileName)
}

// ConfAddHost réserve <sous-réseau>.<octet> pour mac, sous le nom name (DHCP + DNS).
func (l *Lab) ConfAddHost(module, name string, octet int, mac string) error {
	m, err := l.confModule(module)
	if err != nil {
		return err
	}
	mac = strings.ToLower(strings.TrimSpace(mac))
	switch {
	case !reHostName.MatchString(name):
		return fmt.Errorf("nom invalide : %q (minuscules, chiffres, tirets)", name)
	case octet < 2 || octet >= coreconf.DHCPRangeStart:
		return fmt.Errorf("octet entre 2 et %d (hors plage DHCP libre)", coreconf.DHCPRangeStart-1)
	case !reMAC.MatchString(mac):
		return fmt.Errorf("MAC invalide : %q", mac)
	case coreconf.GatewayMAC(m.Env.Subnet) == mac:
		return fmt.Errorf("MAC réservée à la passerelle")
	}
	if _, ok := m.Env.Hosts[name]; ok {
		return fmt.Errorf("%s est une VM du module (vms: du module.yaml)", name)
	}
	if _, ok := m.Conf.Hosts[name]; ok {
		return fmt.Errorf("%s est déjà réservée", name)
	}
	if _, ok := m.Conf.Aliases[name]; ok {
		return fmt.Errorf("%s est déjà un alias", name)
	}
	check := func(n string, o int, mc string) error {
		if o == octet {
			return fmt.Errorf("%s.%d est déjà réservée pour %s", m.Env.Subnet, octet, n)
		}
		if mc != "" && mc == mac {
			return fmt.Errorf("MAC déjà réservée pour %s", n)
		}
		return nil
	}
	for n, o := range m.Env.Hosts {
		if err := check(n, o, m.Env.Macs[n]); err != nil {
			return err
		}
	}
	for n, o := range m.Conf.Hosts {
		if err := check(n, o, m.Conf.HostMACs[n]); err != nil {
			return err
		}
	}
	return saveEdit(m, func(y *mod.Module) { y.AddHost(mod.Host{Name: name, IP: octet, MAC: mac}) })
}

// ConfDelHost retire une réservation (dns.hosts) ; une VM du module, elle, se change dans vms:.
func (l *Lab) ConfDelHost(module, name string) error {
	m, err := l.confModule(module)
	if err != nil {
		return err
	}
	if _, ok := m.Conf.Hosts[name]; !ok {
		if _, env := m.Env.Hosts[name]; env {
			return fmt.Errorf("%s est une VM du module (vms: du module.yaml) : à modifier là", name)
		}
		return fmt.Errorf("pas de réservation %s dans le module.yaml de %s", name, module)
	}
	return saveEdit(m, func(y *mod.Module) { y.DelHost(name) })
}

// ConfAddAlias crée (ou remplace) l'alias name.<domaine> -> target (VM du module ou nom complet).
func (l *Lab) ConfAddAlias(module, name, target string) error {
	m, err := l.confModule(module)
	if err != nil {
		return err
	}
	target = strings.TrimSpace(target)
	if !reHostName.MatchString(name) {
		return fmt.Errorf("nom d'alias invalide : %q", name)
	}
	if _, ok := m.Env.Hosts[name]; ok {
		return fmt.Errorf("%s est déjà le nom d'une VM", name)
	}
	if _, ok := m.Conf.Hosts[name]; ok {
		return fmt.Errorf("%s est déjà une réservation", name)
	}
	_, env := m.Env.Hosts[target]
	_, conf := m.Conf.Hosts[target]
	if !strings.Contains(target, ".") && !env && !conf {
		return fmt.Errorf("cible inconnue dans %s : %q (VM du module, ou nom complet)", module, target)
	}
	if target == name {
		return fmt.Errorf("un alias ne peut pas pointer sur lui-même")
	}
	return saveEdit(m, func(y *mod.Module) { y.SetAlias(name, target) })
}

// ConfDelAlias retire un alias.
func (l *Lab) ConfDelAlias(module, name string) error {
	m, err := l.confModule(module)
	if err != nil {
		return err
	}
	if _, ok := m.Conf.Aliases[name]; !ok {
		return fmt.Errorf("pas d'alias %s dans %s", name, module)
	}
	return saveEdit(m, func(y *mod.Module) { y.DelAlias(name) })
}

// ConfLink ajoute (on) ou retire le lien src -> dst : src peut joindre et résoudre dst.
func (l *Lab) ConfLink(src, dst string, on bool) error {
	m, err := l.confModule(src)
	if err != nil {
		return err
	}
	if _, err := l.confModule(dst); err != nil {
		return err
	}
	if src == dst {
		return fmt.Errorf("un module est toujours relié à lui-même")
	}
	return saveEdit(m, func(y *mod.Module) { y.SetLink(dst, on) })
}
