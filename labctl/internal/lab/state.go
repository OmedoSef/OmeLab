package lab

import (
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"

	"labs/labctl/internal/virt"
)

// GatewayVM est la passerelle du module core.
const GatewayVM = "core-gw"

// DomState renvoie l'état libvirt d'une VM : running, shut off... ou "absente".
func (l *Lab) DomState(name string) string { return l.Hypervisor().State(name) }

// SnapshotsOf renvoie les noms des snapshots d'une VM libvirt.
func (l *Lab) SnapshotsOf(name string) []string { return l.Hypervisor().SnapshotNames(name) }

// NetActive indique si un réseau libvirt est actif.
func (l *Lab) NetActive(name string) bool { return l.Hypervisor().NetActive(name) }

// --- Complétion

// ModuleNames liste les modules qui ont un réseau.
func (l *Lab) ModuleNames() []string {
	mods, _ := l.Modules()
	var out []string
	for _, m := range mods {
		if m.Env != nil {
			out = append(out, m.Name)
		}
	}
	return out
}

// VMs liste les VMs d'un module (VMs à IP fixe + réservations dns.hosts).
func (l *Lab) VMs(module string) []string {
	m, err := l.Module(module)
	if err != nil || m.Env == nil {
		return nil
	}
	vms := m.Env.VMs()
	for h := range m.Conf.Hosts {
		if !slices.Contains(vms, h) && m.Conf.Attached[h] == "" { // VM branchée : elle appartient à son module
			vms = append(vms, h)
		}
	}
	return vms
}

// Snapshots liste les snapshots d'une VM de module.
func (l *Lab) Snapshots(module, vm string) []string {
	return l.SnapshotsOf(module + "-" + vm)
}

// --- État complet (GUI, labctl status)

type State struct {
	Gateway GatewayState  `json:"gateway"`
	Modules []ModuleState `json:"modules"`
}

type GatewayState struct {
	State string `json:"state"`
}

type ModuleState struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Net         string `json:"net"`
	Subnet      string `json:"subnet"`
	Domain      string `json:"domain"`
	NetActive   bool   `json:"netActive"`
	Internet    string `json:"internet"`    // texte brut de « core internet status »
	InternetAll bool   `json:"internetAll"` // ouvert pour tout le module
	// InternetOpen : au moins une VM ouverte à la main. À tester plutôt que le texte Internet (« non branché »
	// n'est pas une ouverture).
	InternetOpen bool            `json:"internetOpen"`
	Vulnerable   bool            `json:"vulnerable"` // vulnerable: true : jamais d'Internet ni rien de sortant
	Links        []string        `json:"links"`      // modules que celui-ci peut joindre
	Commands     []string        `json:"commands"`   // commandes de premier niveau du module
	VMs          []VMState       `json:"vms"`
	Attached     []AttachedState `json:"attached"` // VMs d'autres modules branchées sur ce réseau (attach:)
}

// AttachedState : une VM d'un autre module branchée sur le réseau du module.
type AttachedState struct {
	Name    string `json:"name"`    // nom dans ce module (DNS)
	From    string `json:"from"`    // <module>/<vm>
	Libvirt string `json:"libvirt"` // <module>-<vm>
	IP      string `json:"ip"`
	State   string `json:"state"`
	Plugged bool   `json:"plugged"` // carte présente sur ce réseau
	Local   bool   `json:"local"`   // câblage local (network.local.yaml) : débranchable depuis la GUI
}

type VMState struct {
	Name       string   `json:"name"`
	Libvirt    string   `json:"libvirt"`
	IP         string   `json:"ip"`
	State      string   `json:"state"`
	NoInternet bool     `json:"noInternet"`
	Internet   bool     `json:"internet"` // autorisée à sortir en ce moment
	Snapshots  []string `json:"snapshots"`
}

// Status rassemble l'état de la passerelle et de chaque module.
func (l *Lab) Status() (*State, error) {
	mods, err := l.Modules()
	if err != nil {
		return nil, err
	}
	st := &State{Gateway: GatewayState{State: l.DomState(GatewayVM)}}
	internet := map[string]InternetState{}
	if st.Gateway.State == virt.StateRunning {
		internet = l.internetStatus()
	}
	for _, m := range mods {
		if m.Env == nil {
			continue
		}
		ms := ModuleState{Vulnerable: m.Vulnerable(),
			Name: m.Name, Description: m.Description, Net: m.Env.Net, Subnet: m.Env.Subnet,
			Domain: m.Env.Domain, Links: m.Conf.Links,
		}
		ms.Commands = append(ms.Commands, m.Commands...)
		// État structuré de la passerelle ; le texte (Internet) ne sert qu'à l'affichage
		open := map[string]bool{}
		ms.Internet = "fermé"
		if is, ok := internet[m.Name]; ok {
			ms.Internet, ms.InternetAll = is.String(), is.All
			ms.InternetOpen = is.All || len(is.VMs) > 0
			for _, vm := range is.VMs {
				open[vm] = true
			}
		}
		ms.NetActive = l.NetActive(m.Env.Net)

		vms := l.VMs(m.Name)
		ms.VMs = make([]VMState, len(vms))
		var wg sync.WaitGroup
		for i, vm := range vms {
			wg.Add(1)
			go func() {
				defer wg.Done()
				ip := m.Env.IP(vm)
				if o, ok := m.Conf.Hosts[vm]; ok && ip == "" {
					ip = m.Env.Subnet + "." + strconv.Itoa(o)
				}
				noNet := slices.Contains(m.Env.NoInternet, vm)
				ms.VMs[i] = VMState{
					Name: vm, Libvirt: m.LibvirtName(vm), IP: ip,
					State: l.DomState(m.LibvirtName(vm)), NoInternet: noNet,
					Internet:  !noNet && (ms.InternetAll || open[vm]),
					Snapshots: l.SnapshotsOf(m.LibvirtName(vm)),
				}
			}()
		}
		wg.Wait()
		localRef := map[string]bool{}
		for _, la := range l.localAttachFor(m.Name) {
			localRef[strings.ToLower(la.VM)] = true
		}
		for name, ref := range m.Conf.Attached {
			lv := strings.Replace(ref, "/", "-", 1)
			a := AttachedState{Name: name, From: ref, Libvirt: lv, IP: m.Env.Subnet + "." + strconv.Itoa(m.Conf.Hosts[name]), State: l.DomState(lv), Local: localRef[strings.ToLower(ref)]}
			if ifs, err := l.Hypervisor().Interfaces(lv); err == nil && m.Conf.HostMACs[name] != "" {
				for _, it := range ifs {
					a.Plugged = a.Plugged || strings.EqualFold(it.MAC, m.Conf.HostMACs[name])
				}
			}
			ms.Attached = append(ms.Attached, a)
		}
		sort.Slice(ms.Attached, func(i, j int) bool { return ms.Attached[i].Name < ms.Attached[j].Name })
		st.Modules = append(st.Modules, ms)
	}
	return st, nil
}

// internetStatus renvoie l'état Internet de chaque module, par module.
func (l *Lab) internetStatus() map[string]InternetState {
	out := map[string]InternetState{}
	g := l.Gateway(nil)
	if !g.Running() {
		return out
	}
	st, err := g.Internet()
	if err != nil {
		return out
	}
	for _, s := range st {
		out[s.Module] = s
	}
	return out
}
