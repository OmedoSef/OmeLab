package lab

import (
	"bufio"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
)

const (
	libvirtURI = "qemu:///system"
	GatewayVM  = "core-gw"
)

// virsh lance une commande virsh en sortie non traduite et renvoie sa sortie (vide si erreur).
func virsh(args ...string) (string, error) {
	cmd := exec.Command("virsh", append([]string{"-c", libvirtURI}, args...)...)
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	out, err := cmd.Output()
	return strings.TrimSpace(string(out)), err
}

// DomState renvoie l'état libvirt d'une VM : running, shut off... ou "absente".
func DomState(name string) string {
	s, err := virsh("domstate", name)
	if err != nil {
		return "absente"
	}
	return strings.SplitN(s, "\n", 2)[0]
}

// Snapshots renvoie les snapshots d'une VM libvirt.
func SnapshotsOf(name string) []string {
	s, err := virsh("snapshot-list", "--name", name)
	if err != nil || s == "" {
		return nil
	}
	return strings.Fields(s)
}

// NetActive indique si un réseau libvirt est actif.
func NetActive(name string) bool {
	s, err := virsh("net-info", name)
	if err != nil {
		return false
	}
	for _, l := range strings.Split(s, "\n") {
		if f := strings.Fields(l); len(f) == 2 && f[0] == "Active:" {
			return f[1] == "yes"
		}
	}
	return false
}

// --- Complétion

// ModuleNames liste les modules qui ont un réseau (module.env).
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

// VMs liste les VMs d'un module (module.env + réservations de core.conf).
func (l *Lab) VMs(module string) []string {
	m, err := l.Module(module)
	if err != nil || m.Env == nil {
		return nil
	}
	vms := m.Env.VMs()
	for h := range m.Conf.Hosts {
		if !contains(vms, h) {
			vms = append(vms, h)
		}
	}
	return vms
}

// Snapshots liste les snapshots d'une VM de module.
func (l *Lab) Snapshots(module, vm string) []string {
	return SnapshotsOf(module + "-" + vm)
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
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Net         string    `json:"net"`
	Subnet      string    `json:"subnet"`
	Domain      string    `json:"domain"`
	NetActive   bool      `json:"netActive"`
	Internet    string    `json:"internet"`    // texte brut de « core internet status »
	InternetAll bool      `json:"internetAll"` // ouvert pour tout le module
	Links       []string  `json:"links"`       // modules que celui-ci peut joindre
	Commands    []string  `json:"commands"`    // commandes de premier niveau du module
	VMs         []VMState `json:"vms"`
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

var reOpen = regexp.MustCompile(`^OUVERT : (.*?)(?: \(jamais : .*\))?$`)

// Status rassemble l'état de la passerelle et de chaque module.
func (l *Lab) Status() (*State, error) {
	mods, err := l.Modules()
	if err != nil {
		return nil, err
	}
	st := &State{Gateway: GatewayState{State: DomState(GatewayVM)}}
	internet := map[string]string{}
	if st.Gateway.State == "running" {
		internet = l.internetStatus()
	}
	for _, m := range mods {
		if m.Env == nil {
			continue
		}
		ms := ModuleState{
			Name: m.Name, Description: m.Description, Net: m.Env.Net, Subnet: m.Env.Subnet,
			Domain: m.Env.Domain, Links: m.Conf.Links, Internet: internet[m.Name],
		}
		for c := range m.Commands.Children {
			ms.Commands = append(ms.Commands, c)
		}
		if ms.Internet == "" {
			ms.Internet = "fermé"
		}
		open := map[string]bool{}
		if r := reOpen.FindStringSubmatch(ms.Internet); r != nil {
			if r[1] == "tout le module" {
				ms.InternetAll = true
			}
			for _, vm := range strings.Fields(r[1]) {
				open[vm] = true
			}
		}
		ms.NetActive = NetActive(m.Env.Net)

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
				noNet := contains(m.Env.NoInternet, vm)
				ms.VMs[i] = VMState{
					Name: vm, Libvirt: m.LibvirtName(vm), IP: ip,
					State: DomState(m.LibvirtName(vm)), NoInternet: noNet,
					Internet:  !noNet && (ms.InternetAll || open[vm]),
					Snapshots: SnapshotsOf(m.LibvirtName(vm)),
				}
			}()
		}
		wg.Wait()
		st.Modules = append(st.Modules, ms)
	}
	return st, nil
}

// internetStatus interroge la passerelle via « lab.sh core internet status » : module -> état.
func (l *Lab) internetStatus() map[string]string {
	out := map[string]string{}
	cmd := l.Command("core", "internet", "status")
	cmd.Env = append(os.Environ(), "LC_ALL=C.UTF-8")
	b, err := cmd.Output()
	if err != nil {
		return out
	}
	sc := bufio.NewScanner(strings.NewReader(string(b)))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) >= 3 && f[0] != "MODULE" {
			out[f[0]] = strings.Join(f[2:], " ")
		}
	}
	return out
}
