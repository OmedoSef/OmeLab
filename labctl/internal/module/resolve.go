package module

import (
	"fmt"
	"net/netip"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Settings lit un réglage (variable d'environnement, config.local.env, config.env), fallback sinon.
type Settings func(name, fallback string) string

// Valeurs intégrées (dernier niveau de défaut)
const (
	BuiltinImage  = "debian13"
	BuiltinCPUs   = 1
	BuiltinMemory = 2048
	BuiltinDisk   = 20
)

// Origine d'une valeur effective
const (
	FromVM       = "vm"
	FromDefaults = "defaults du module"
	FromSettings = "réglages"
	FromBuiltin  = "intégré"
)

// Effective est une VM avec toutes ses valeurs résolues (défauts appliqués).
type Effective struct {
	Name     string
	Role     string
	IP       int // 0 = DHCP libre
	MAC      string
	Image    string  // "" si Source
	Source   *Source // nil si Image
	CPUs     int
	Memory   int
	Disk     int
	Hardware Hardware
	Agent    bool
	Internet string
	Snapshot string
	Keyboard string            // "" = disposition de l'hôte (résolue par labctl au moment de la création)
	VM       *VM               // définition d'origine (paquets, fichiers, certificats...)
	Origins  map[string]string // champ -> origine
}

// NetworkName renvoie le nom du réseau du module (défaut <module>-lab).
func (m *Module) NetworkName() string {
	if m.Network != nil && m.Network.Name != "" {
		return m.Network.Name
	}
	return m.Module + "-lab"
}

// Domain renvoie le domaine du module. Trois cas, dans l'ordre : network.domain (domaine complet, pris tel
// quel), network.subdomain (<subdomain>.<racine>, pour se coller sous le domaine racine), sinon le défaut
// <réseau>.<racine>.
func (m *Module) Domain(rootDomain string) string {
	if m.Network != nil {
		if m.Network.Domain != "" {
			return m.Network.Domain
		}
		if m.Network.Subdomain != "" {
			return m.Network.Subdomain + "." + rootDomain
		}
	}
	return m.NetworkName() + "." + rootDomain
}

// Subnet renvoie le /24 du module (invalide si absent : choisi au premier up).
func (m *Module) Subnet() netip.Prefix {
	if m.Network == nil {
		return netip.Prefix{}
	}
	p, err := netip.ParsePrefix(m.Network.Subnet)
	if err != nil {
		return netip.Prefix{}
	}
	return p.Masked()
}

// Prefix3 renvoie les trois premiers octets du sous-réseau (ex. 10.66.66), "" si inconnu.
func (m *Module) Prefix3() string {
	p := m.Subnet()
	if !p.IsValid() {
		return ""
	}
	b := p.Addr().As4()
	return fmt.Sprintf("%d.%d.%d", b[0], b[1], b[2])
}

// VMNames renvoie les VMs dans l'ordre du fichier.
func (m *Module) VMNames() []string {
	if len(m.VMOrder) == len(m.VMs) {
		return m.VMOrder
	}
	names := make([]string, 0, len(m.VMs))
	for n := range m.VMs {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// Effective résout une VM : VM > defaults: du module > réglages > intégré.
func (m *Module) Effective(name string, set Settings) Effective {
	vm := m.VMs[name]
	if vm == nil {
		vm = &VM{}
	}
	d := m.Defaults
	e := Effective{Name: name, Role: vm.Role, IP: vm.IP, MAC: strings.ToLower(vm.MAC), Source: vm.Source, VM: vm, Origins: map[string]string{}}
	settingInt := func(k string) (int, bool) {
		if set == nil {
			return 0, false
		}
		n, err := strconv.Atoi(set(k, ""))
		return n, err == nil && n > 0
	}
	pickInt := func(field string, v, dv int, key string, builtin int) int {
		switch {
		case v > 0:
			e.Origins[field] = FromVM
			return v
		case dv > 0:
			e.Origins[field] = FromDefaults
			return dv
		}
		if n, ok := settingInt(key); ok {
			e.Origins[field] = FromSettings
			return n
		}
		e.Origins[field] = FromBuiltin
		return builtin
	}
	pickStr := func(field, v, dv, builtin string) string {
		switch {
		case v != "":
			e.Origins[field] = FromVM
			return v
		case dv != "":
			e.Origins[field] = FromDefaults
			return dv
		}
		e.Origins[field] = FromBuiltin
		return builtin
	}

	if vm.Source == nil && vm.Windows == nil {
		e.Image = pickStr("image", vm.Image, d.Image, "")
		if e.Image == "" {
			if set != nil && set("LABS_DEFAULT_IMAGE", "") != "" {
				e.Image, e.Origins["image"] = set("LABS_DEFAULT_IMAGE", ""), FromSettings
			} else {
				e.Image, e.Origins["image"] = BuiltinImage, FromBuiltin
			}
		}
	}
	e.CPUs = pickInt("cpus", vm.CPUs, d.CPUs, "LABS_DEFAULT_CPUS", BuiltinCPUs)
	e.Memory = pickInt("memory", vm.Memory, d.Memory, "LABS_DEFAULT_MEMORY", BuiltinMemory)
	e.Disk = pickInt("disk", vm.Disk, d.Disk, "LABS_DEFAULT_DISK", BuiltinDisk)
	e.Internet = pickStr("internet", vm.Internet, d.Internet, "off")
	e.Snapshot = pickStr("snapshot", vm.Snapshot, d.Snapshot, "")
	e.Keyboard = pickStr("keyboard", vm.Keyboard, d.Keyboard, "") // "" : clavier de l'hôte, appliqué à la création
	h, dh := vm.Hardware, d.Hardware
	e.Hardware = Hardware{
		Machine:    pickStr("hardware.machine", h.Machine, dh.Machine, "q35"),
		DiskBus:    pickStr("hardware.disk_bus", h.DiskBus, dh.DiskBus, "virtio"),
		NICModel:   pickStr("hardware.nic_model", h.NICModel, dh.NICModel, "virtio"),
		Graphics:   pickStr("hardware.graphics", h.Graphics, dh.Graphics, "spice"),
		Video:      pickStr("hardware.video", h.Video, dh.Video, "virtio"),
		OSInfo:     pickStr("hardware.osinfo", h.OSInfo, dh.OSInfo, ""),
		Firmware:   pickStr("hardware.firmware", h.Firmware, dh.Firmware, map[bool]string{true: "efi", false: "bios"}[vm.Windows != nil]),
		SecureBoot: h.SecureBoot || dh.SecureBoot,
		TPM:        h.TPM || dh.TPM,
	}
	switch {
	case vm.Guest.Agent != nil:
		e.Agent, e.Origins["guest.agent"] = *vm.Guest.Agent, FromVM
	case d.Guest.Agent != nil:
		e.Agent, e.Origins["guest.agent"] = *d.Guest.Agent, FromDefaults
	default: // images cloud (cloud-init) et Windows (installé à la fin de l'installation) : agent présent
		e.Agent, e.Origins["guest.agent"] = e.Image != "" || vm.Windows != nil, FromBuiltin
	}
	return e
}

// ---------------------------------------------------------------- variables ${...}

var reVar = regexp.MustCompile(`\$\{([^}]*)\}`)

// VarContext donne les valeurs des variables pour une VM d'un module.
type VarContext struct {
	Module   *Module
	VM       string            // VM courante ("" hors VM)
	Root     string            // domaine racine des labs
	Keyboard string
	LabVars  map[string]string // vars: du lab.yaml : surchargent le vars: du module (même nom)
}

// lookup renvoie la valeur d'une variable, ou une erreur explicite.
func (c VarContext) lookup(name string) (string, error) {
	m := c.Module
	domain := m.Domain(c.Root)
	ipOf := func(vm string) (string, error) {
		v, ok := m.VMs[vm]
		if !ok {
			return "", fmt.Errorf("VM inconnue : %s", vm)
		}
		if v.IP == 0 {
			return "", fmt.Errorf("la VM %s n'a pas d'IP fixe (ip:)", vm)
		}
		if m.Prefix3() == "" {
			return "", fmt.Errorf("sous-réseau du module encore inconnu")
		}
		return fmt.Sprintf("%s.%d", m.Prefix3(), v.IP), nil
	}
	switch {
	case name == "module.name":
		return m.Module, nil
	case name == "module.domain":
		return domain, nil
	case name == "network.subnet":
		if !m.Subnet().IsValid() {
			return "", fmt.Errorf("sous-réseau du module encore inconnu")
		}
		return m.Subnet().String(), nil
	case name == "network.gateway":
		if m.Prefix3() == "" {
			return "", fmt.Errorf("sous-réseau du module encore inconnu")
		}
		return m.Prefix3() + ".254", nil
	case name == "keyboard":
		return c.Keyboard, nil
	case strings.HasPrefix(name, "var."):
		key := strings.TrimPrefix(name, "var.")
		if v, ok := c.LabVars[key]; ok { // le lab.yaml a le dernier mot sur le défaut du module
			return v, nil
		}
		if v, ok := m.Vars[key]; ok {
			return v, nil
		}
		return "", fmt.Errorf("variable ${var.%s} absente de vars: (ni du lab, ni du module)", key)
	case strings.HasPrefix(name, "vm."):
		if c.VM == "" {
			return "", fmt.Errorf("${%s} n'a de sens que pour une VM", name)
		}
		return c.lookupVM(c.VM, strings.TrimPrefix(name, "vm."), domain, ipOf)
	case strings.HasPrefix(name, "vms."):
		vm, field, ok := strings.Cut(strings.TrimPrefix(name, "vms."), ".")
		if !ok {
			return "", fmt.Errorf("forme attendue : ${vms.<vm>.ip}")
		}
		if _, exists := m.VMs[vm]; !exists {
			return "", fmt.Errorf("VM inconnue : %s", vm)
		}
		return c.lookupVM(vm, field, domain, ipOf)
	}
	return "", fmt.Errorf("variable inconnue ${%s} (module.name, module.domain, network.subnet, network.gateway, vm.name, vm.ip, vm.fqdn, vms.<vm>.ip, vms.<vm>.fqdn, keyboard, var.<nom>)", name)
}

func (c VarContext) lookupVM(vm, field, domain string, ipOf func(string) (string, error)) (string, error) {
	switch field {
	case "name":
		return vm, nil
	case "ip":
		return ipOf(vm)
	case "fqdn":
		return vm + "." + domain, nil
	}
	return "", fmt.Errorf("champ de VM inconnu : %s (name, ip, fqdn)", field)
}

// Expand remplace les ${...} d'un texte ; renvoie la première erreur rencontrée.
func (c VarContext) Expand(text string) (string, error) {
	var firstErr error
	out := reVar.ReplaceAllStringFunc(text, func(match string) string {
		v, err := c.lookup(reVar.FindStringSubmatch(match)[1])
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			return match
		}
		return v
	})
	return out, firstErr
}

// Vars liste les variables ${...} d'un texte (pour la validation).
func Vars(text string) []string {
	var out []string
	for _, m := range reVar.FindAllStringSubmatch(text, -1) {
		out = append(out, m[1])
	}
	return out
}
