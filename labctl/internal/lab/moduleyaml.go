package lab

import (
	"fmt"
	"net/netip"
	"os"
	"sort"
	"strings"

	"labs/labctl/internal/module"
	"labs/labctl/internal/virt"
)

// Les modules déclaratifs (module.yaml) sont la source de vérité. Pour le code qui lit encore l'ancien
// format (module.env, core.conf), Env et Conf sont dérivés du YAML ; le bash y accède par
// « labctl module env|conf ».

// loadYAML complète un module à partir de son module.yaml, s'il en a un.
func (l *Lab) loadYAML(m *Module) {
	y, err := module.Load(m.Dir)
	if err == module.ErrNoModule {
		return
	}
	m.HasYAML = true
	if err != nil {
		m.YAMLErr = err
		return
	}
	m.YAML = y
	l.injectLocalAttach(y) // câblage local (attach.local.yaml), traité ensuite comme les attach: du module.yaml
	// Résout les variables ${...} dans toute la config (sauf module/description/vars) : tout ce qui suit
	// (Env, Conf, pare-feu, création des VMs) travaille sur les valeurs finales. Tolérant au sous-réseau pas
	// encore assigné. Une variable fautive (faute de frappe, cycle) devient une erreur du module.
	if err := y.ExpandVars(l.pkiDomain(), l.Keyboard(), l.LabVars()); err != nil {
		m.YAMLErr = err
		return
	}
	if y.Description != "" {
		m.Description = y.Description
	}
	if y.Prefix3() == "" { // sous-réseau pas encore choisi : pas de réseau utilisable
		return
	}
	set := module.Settings(l.Setting)
	env := &Env{
		Net: y.NetworkName(), Subnet: y.Prefix3(), Domain: y.Domain(l.pkiDomain()),
		Hosts: map[string]int{}, Macs: map[string]string{},
	}
	for _, name := range y.VMNames() {
		e := y.Effective(name, set)
		if e.IP > 0 {
			env.Hosts[name] = e.IP
		}
		if e.MAC != "" {
			env.Macs[name] = e.MAC
		}
		if e.Internet == "never" {
			env.NoInternet = append(env.NoInternet, name)
		}
	}
	m.Env = env
	conf := &CoreConf{Hosts: map[string]int{}, HostMACs: map[string]string{}, Aliases: map[string]string{}, Links: y.DNS.Links, Attached: map[string]string{}}
	// VMs branchées depuis d'autres modules : réservation (sans route ni DNS, voir coreconf) et nom dans ce module
	for _, a := range y.Attach {
		_, vm := a.Split()
		conf.Hosts[vm], conf.HostMACs[vm], conf.Attached[vm] = a.IP, strings.ToLower(a.MAC), a.VM
	}
	for _, h := range y.DNS.Hosts {
		conf.Hosts[h.Name], conf.HostMACs[h.Name] = h.IP, strings.ToLower(h.MAC)
	}
	for a, t := range y.DNS.Aliases {
		conf.Aliases[a] = t
	}
	m.Conf = conf
}

// ModuleContext rassemble ce que la validation d'un module doit savoir de son environnement.
func (l *Lab) ModuleContext(name string) module.Context {
	ctx := module.Context{RootDomain: l.pkiDomain(), Images: ImageNames(), Settings: l.Setting}
	mods, _ := l.Modules()
	moduleNets := map[string]bool{}
	for _, m := range mods {
		if m.Env == nil {
			continue
		}
		ctx.Modules = append(ctx.Modules, m.Name)
		if ctx.Targets == nil {
			ctx.Targets = map[string][]string{}
		}
		for h := range m.Env.Hosts {
			ctx.Targets[m.Name] = append(ctx.Targets[m.Name], h)
		}
		for h := range m.Conf.Hosts {
			if m.Conf.Attached[h] == "" { // une VM branchée appartient à son module, pas à celui-ci
				ctx.Targets[m.Name] = append(ctx.Targets[m.Name], h)
			}
		}
		moduleNets[m.Env.Net] = true
		if m.Name == name {
			continue
		}
		p, _ := netip.ParsePrefix(m.Env.Subnet + ".0/24")
		ctx.Used = append(ctx.Used, module.NetInfo{Owner: "module " + m.Name, Name: m.Env.Net, Subnet: p, Domain: m.Env.Domain})
	}
	// Réseaux libvirt qui n'appartiennent à aucun module (default...)
	if nets, err := virt.Shared().Networks(); err == nil {
		for _, n := range nets {
			if moduleNets[n.Name] {
				continue
			}
			info := module.NetInfo{Owner: "réseau libvirt", Name: n.Name, Domain: n.Domain}
			if a, err := netip.ParseAddr(n.HostIP); err == nil && n.Prefix > 0 {
				info.Subnet = netip.PrefixFrom(a, n.Prefix).Masked()
			}
			ctx.Used = append(ctx.Used, info)
		}
	}
	if b, err := os.ReadFile("/proc/meminfo"); err == nil {
		var kb int
		fmt.Sscanf(strings.TrimPrefix(strings.SplitN(string(b), "\n", 2)[0], "MemTotal:"), "%d", &kb)
		ctx.HostRAM = kb / 1024
	}
	sort.Strings(ctx.Modules)
	return ctx
}

// ValidateModule lit et valide le module.yaml d'un module.
func (l *Lab) ValidateModule(name string) (*module.Module, []module.Issue, error) {
	m, err := l.Module(name)
	if err != nil {
		return nil, nil, err
	}
	if !m.HasYAML {
		return nil, nil, fmt.Errorf("le module %s n'a pas de %s", name, module.FileName)
	}
	if m.YAMLErr != nil {
		return nil, []module.Issue{{Error: true, Msg: m.YAMLErr.Error()}}, nil
	}
	return m.YAML, m.YAML.Validate(l.ModuleContext(name)), nil
}
