// Package virtfake est un faux libvirt en mémoire (virt.Hypervisor) pour tester le moteur de labctl sans hyperviseur :
// les VMs et réseaux sont des structures que le test prépare (AddVM, AddNetwork) puis inspecte ; chaque opération
// qui modifie l'état est notée dans Calls, Fail simule la panne d'une opération et IPs l'adresse prise par une VM à
// sa définition. Les arrêts et démarrages sont instantanés, il n'y a ni disque ni agent.
package virtfake

import (
	"encoding/xml"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"labs/labctl/internal/virt"
)

// VM est une VM du faux hyperviseur.
type VM struct {
	Name, State, Desc string
	CPUs, MemMiB      int
	EFI               bool
	IP                string // adresse annoncée (IPv4, Addresses) ; "" = aucune
	Ifaces            []virt.Interface
	Disks             []string
	Snaps             []virt.Snapshot
}

// Fake implémente virt.Hypervisor.
type Fake struct {
	mu    sync.Mutex
	vms   map[string]*VM
	nets  map[string]*virt.Network
	Calls []string // opérations qui modifient l'état, ex. "start netlab-cli", "define-net lab-netlab"
	// Fail fait échouer une opération : clé « <op> <VM> » (ops : start, shutdown, destroy, reboot, undefine,
	// disks, interfaces), valeur = erreur renvoyée. À remplir avant l'appel, sans concurrence.
	Fail map[string]error
	// IPs donne une adresse à une VM dès sa définition (DefineDomain), comme le ferait son DHCP.
	IPs   map[string]string
	clock time.Time
}

var _ virt.Hypervisor = (*Fake)(nil)

// New renvoie un hyperviseur vide.
func New() *Fake {
	return &Fake{vms: map[string]*VM{}, nets: map[string]*virt.Network{}, clock: time.Unix(1_700_000_000, 0)}
}

// AddVM déclare une VM existante (état virt.StateRunning, "shut off"…).
func (f *Fake) AddVM(vm VM) *Fake {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := vm
	f.vms[vm.Name] = &c
	return f
}

// AddNetwork déclare un réseau existant.
func (f *Fake) AddNetwork(n virt.Network) *Fake {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := n
	f.nets[n.Name] = &c
	return f
}

// VM renvoie une copie de la VM name (nil si absente).
func (f *Fake) VM(name string) *VM {
	f.mu.Lock()
	defer f.mu.Unlock()
	d, ok := f.vms[name]
	if !ok {
		return nil
	}
	c := *d
	c.Ifaces, c.Snaps = slices.Clone(d.Ifaces), slices.Clone(d.Snaps)
	return &c
}

// Network renvoie une copie du réseau name (nil si absent).
func (f *Fake) Network(name string) *virt.Network {
	f.mu.Lock()
	defer f.mu.Unlock()
	n, ok := f.nets[name]
	if !ok {
		return nil
	}
	c := *n
	return &c
}

func (f *Fake) note(format string, a ...any) { f.Calls = append(f.Calls, fmt.Sprintf(format, a...)) }

func notFound(kind, name string) error { return fmt.Errorf("%s %s %w", kind, name, virt.ErrNotFound) }

// vm renvoie la VM name ; f.mu doit être tenu.
func (f *Fake) vm(name string) (*VM, error) {
	if d, ok := f.vms[name]; ok {
		return d, nil
	}
	return nil, notFound("VM", name)
}

// withVM applique op à la VM name sous verrou.
func (f *Fake) withVM(name string, op func(*VM) error) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	d, err := f.vm(name)
	if err != nil {
		return err
	}
	return op(d)
}

// ---------------------------------------------------------------- domaines

func (f *Fake) State(name string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if d, ok := f.vms[name]; ok {
		return d.State
	}
	return virt.StateAbsent
}

func (f *Fake) Domains() ([]virt.Domain, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []virt.Domain
	for _, d := range f.vms {
		out = append(out, virt.Domain{Name: d.Name, State: d.State, Description: d.Desc})
	}
	slices.SortFunc(out, func(a, b virt.Domain) int { return strings.Compare(a.Name, b.Name) })
	return out, nil
}

func (f *Fake) Description(name string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if d, ok := f.vms[name]; ok {
		return d.Desc
	}
	return ""
}

// DomainXML produit une définition minimale, relisible par DefineDomain.
func (f *Fake) DomainXML(name string, _ bool) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	d, err := f.vm(name)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	esc := func(s string) string { var e strings.Builder; _ = xml.EscapeText(&e, []byte(s)); return e.String() }
	fmt.Fprintf(&b, "<domain type='kvm'>\n  <name>%s</name>\n  <description>%s</description>\n", esc(d.Name), esc(d.Desc))
	fmt.Fprintf(&b, "  <memory unit='KiB'>%d</memory>\n  <vcpu>%d</vcpu>\n", d.MemMiB*1024, d.CPUs)
	if d.EFI {
		b.WriteString("  <os firmware='efi'/>\n")
	}
	b.WriteString("  <devices>\n")
	for _, k := range d.Disks {
		fmt.Fprintf(&b, "    <disk type='file' device='disk'><source file='%s'/></disk>\n", esc(k))
	}
	for _, i := range d.Ifaces {
		fmt.Fprintf(&b, "    <interface type='%s'><mac address='%s'/><source network='%s'/><model type='%s'/></interface>\n",
			esc(i.Type), esc(i.MAC), esc(i.Network), esc(i.Model))
	}
	b.WriteString("  </devices>\n</domain>\n")
	return b.String(), nil
}

// DefineDomain crée ou remplace une VM d'après sa définition (nom, vCPU, mémoire, cartes, disques, description).
func (f *Fake) DefineDomain(x string) error {
	var dx struct {
		Name string `xml:"name"`
		Desc string `xml:"description"`
		OS   struct {
			Firmware string    `xml:"firmware,attr"`
			Loader   *struct{} `xml:"loader"`
		} `xml:"os"`
		Disks []struct {
			Source struct {
				File string `xml:"file,attr"`
			} `xml:"source"`
		} `xml:"devices>disk"`
	}
	if err := xml.Unmarshal([]byte(x), &dx); err != nil {
		return err
	}
	if dx.Name == "" {
		return fmt.Errorf("définition sans nom")
	}
	cpus, mem, err := virt.ResourcesFromXML(x)
	if err != nil {
		return err
	}
	ifs, err := virt.InterfacesFromXML(x)
	if err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	d, ok := f.vms[dx.Name]
	if !ok {
		d = &VM{Name: dx.Name, State: "shut off", IP: f.IPs[dx.Name]}
		f.vms[dx.Name] = d
	}
	d.Desc, d.CPUs, d.MemMiB, d.Ifaces, d.EFI = strings.TrimSpace(dx.Desc), cpus, mem, ifs, dx.OS.Firmware == "efi" || dx.OS.Loader != nil
	d.Disks = nil
	for _, k := range dx.Disks {
		if k.Source.File != "" {
			d.Disks = append(d.Disks, k.Source.File)
		}
	}
	f.note("define %s", dx.Name)
	return nil
}

func (f *Fake) setState(op, name, want string, from ...string) error {
	return f.withVM(name, func(d *VM) error {
		if err := f.Fail[op+" "+name]; err != nil {
			return err
		}
		if len(from) > 0 && !slices.Contains(from, d.State) {
			return fmt.Errorf("%s %s : VM %s", op, name, d.State)
		}
		d.State = want
		f.note("%s %s", op, name)
		return nil
	})
}

func (f *Fake) StartDomain(name string) error {
	return f.setState("start", name, virt.StateRunning, "shut off", "crashed")
}
func (f *Fake) ShutdownDomain(name string) error {
	return f.setState("shutdown", name, "shut off", virt.StateRunning)
}
func (f *Fake) DestroyDomain(name string) error {
	return f.setState("destroy", name, "shut off", virt.StateRunning, "paused", "in shutdown")
}
func (f *Fake) RebootDomain(name string) error {
	return f.setState("reboot", name, virt.StateRunning, virt.StateRunning)
}

func (f *Fake) UndefineDomain(name string) error {
	return f.withVM(name, func(d *VM) error {
		if err := f.Fail["undefine "+name]; err != nil {
			return err
		}
		if d.State == virt.StateRunning {
			return fmt.Errorf("undefine %s : VM en marche", name)
		}
		delete(f.vms, name)
		f.note("undefine %s", name)
		return nil
	})
}

func (f *Fake) IsEFI(name string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	d, ok := f.vms[name]
	return ok && d.EFI
}

func (f *Fake) Resources(name string) (cpus, memMiB int, err error) {
	err = f.withVM(name, func(d *VM) error { cpus, memMiB = d.CPUs, d.MemMiB; return nil })
	return
}

func (f *Fake) SetResources(name string, cpus, memMiB int) error {
	return f.withVM(name, func(d *VM) error {
		d.CPUs, d.MemMiB = cpus, memMiB
		f.note("resources %s %d %d", name, cpus, memMiB)
		return nil
	})
}

func (f *Fake) Disks(name string) (out []string, err error) {
	err = f.withVM(name, func(d *VM) error {
		if err := f.Fail["disks "+name]; err != nil {
			return err
		}
		out = slices.Clone(d.Disks)
		return nil
	})
	return
}

func (f *Fake) RemoveCDROMs(name string) error {
	return f.withVM(name, func(*VM) error { f.note("remove-cdroms %s", name); return nil })
}

func (f *Fake) SendKey(name string, code uint32) error {
	return f.withVM(name, func(*VM) error { f.note("sendkey %s %d", name, code); return nil })
}

// ---------------------------------------------------------------- cartes réseau et adresses

func (f *Fake) Interfaces(name string) (out []virt.Interface, err error) {
	err = f.withVM(name, func(d *VM) error {
		if err := f.Fail["interfaces "+name]; err != nil {
			return err
		}
		out = slices.Clone(d.Ifaces)
		return nil
	})
	return
}

func (f *Fake) AttachInterface(name, network, mac, model string, live bool) error {
	return f.withVM(name, func(d *VM) error {
		if _, ok := f.nets[network]; !ok {
			return notFound("réseau", network)
		}
		d.Ifaces = append(d.Ifaces, virt.Interface{Type: "network", Network: network, MAC: strings.ToLower(mac), Model: model})
		f.note("attach %s %s %s", name, network, strings.ToLower(mac))
		return nil
	})
}

func (f *Fake) DetachInterface(name, mac string, live bool) error {
	return f.withVM(name, func(d *VM) error {
		d.Ifaces = slices.DeleteFunc(d.Ifaces, func(i virt.Interface) bool { return strings.EqualFold(i.MAC, mac) })
		f.note("detach %s %s", name, strings.ToLower(mac))
		return nil
	})
}

func (f *Fake) Addresses(name string, _ uint32) (out []virt.Address, err error) {
	err = f.withVM(name, func(d *VM) error {
		if d.IP != "" && len(d.Ifaces) > 0 {
			out = []virt.Address{{Iface: "eth0", MAC: d.Ifaces[0].MAC, IP: d.IP}}
		}
		return nil
	})
	return
}

func (f *Fake) IPv4(name string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if d, ok := f.vms[name]; ok && d.State == virt.StateRunning {
		return d.IP
	}
	return ""
}

// ---------------------------------------------------------------- snapshots

func (f *Fake) Snapshots(name string) (out []virt.Snapshot, err error) {
	err = f.withVM(name, func(d *VM) error { out = slices.Clone(d.Snaps); return nil })
	return
}

func (f *Fake) SnapshotNames(name string) []string {
	snaps, _ := f.Snapshots(name)
	out := make([]string, len(snaps))
	for i, s := range snaps {
		out[i] = s.Name
	}
	return out
}

func (f *Fake) CreateSnapshot(name, snap, desc string) error {
	return f.withVM(name, func(d *VM) error {
		if slices.ContainsFunc(d.Snaps, func(s virt.Snapshot) bool { return s.Name == snap }) {
			return fmt.Errorf("le snapshot %s de %s existe déjà", snap, name)
		}
		f.clock = f.clock.Add(time.Minute)
		d.Snaps = append(d.Snaps, virt.Snapshot{Name: snap, Created: f.clock, State: d.State, Desc: desc})
		f.note("snapshot %s %s", name, snap)
		return nil
	})
}

func (f *Fake) snapIndex(d *VM, snap string) (int, error) {
	i := slices.IndexFunc(d.Snaps, func(s virt.Snapshot) bool { return s.Name == snap })
	if i < 0 {
		return 0, notFound("snapshot "+snap+" de", d.Name)
	}
	return i, nil
}

// RevertSnapshot remet la VM dans l'état (allumée ou éteinte) qu'elle avait au snapshot.
func (f *Fake) RevertSnapshot(name, snap string) error {
	return f.withVM(name, func(d *VM) error {
		i, err := f.snapIndex(d, snap)
		if err != nil {
			return err
		}
		d.State = d.Snaps[i].State
		f.note("revert %s %s", name, snap)
		return nil
	})
}

func (f *Fake) DeleteSnapshot(name, snap string) error {
	return f.withVM(name, func(d *VM) error {
		i, err := f.snapIndex(d, snap)
		if err != nil {
			return err
		}
		d.Snaps = slices.Delete(d.Snaps, i, i+1)
		f.note("delete-snapshot %s %s", name, snap)
		return nil
	})
}

// ---------------------------------------------------------------- réseaux

func (f *Fake) Networks() ([]virt.Network, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []virt.Network
	for _, n := range f.nets {
		out = append(out, *n)
	}
	slices.SortFunc(out, func(a, b virt.Network) int { return strings.Compare(a.Name, b.Name) })
	return out, nil
}

func (f *Fake) NetExists(name string) bool { return f.Network(name) != nil }

func (f *Fake) NetActive(name string) bool {
	n := f.Network(name)
	return n != nil && n.Active
}

func (f *Fake) NetXML(name string, _ bool) (string, error) {
	n := f.Network(name)
	if n == nil {
		return "", notFound("réseau", name)
	}
	fw := ""
	if n.Forward != "" {
		fw = fmt.Sprintf("<forward mode='%s'/>", n.Forward)
	}
	return fmt.Sprintf("<network><name>%s</name>%s<bridge name='%s'/><ip address='%s' prefix='%d'/></network>",
		n.Name, fw, n.Bridge, n.HostIP, n.Prefix), nil
}

// DefineNetwork crée ou remplace un réseau (inactif tant qu'il n'est pas démarré).
func (f *Fake) DefineNetwork(x string) error {
	var nx virt.NetworkXML
	if err := xml.Unmarshal([]byte(x), &nx); err != nil {
		return err
	}
	if nx.Name == "" {
		return fmt.Errorf("définition de réseau sans nom")
	}
	n := virt.Network{Name: nx.Name}
	virt.NetworkFromXML(&n, x)
	f.mu.Lock()
	defer f.mu.Unlock()
	if old, ok := f.nets[n.Name]; ok {
		n.Active, n.Autostart = old.Active, old.Autostart
	}
	f.nets[n.Name] = &n
	f.note("define-net %s", n.Name)
	return nil
}

func (f *Fake) StartNetwork(name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	n, ok := f.nets[name]
	if !ok {
		return notFound("réseau", name)
	}
	if !n.Active {
		f.note("start-net %s", name)
	}
	n.Active, n.Autostart = true, true
	return nil
}

func (f *Fake) DeleteNetwork(name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.nets[name]; !ok {
		return notFound("réseau", name)
	}
	delete(f.nets, name)
	f.note("delete-net %s", name)
	return nil
}

func (f *Fake) NetworkUsers(name string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, d := range f.vms {
		if slices.ContainsFunc(d.Ifaces, func(i virt.Interface) bool { return i.Type == "network" && i.Network == name }) {
			out = append(out, d.Name)
		}
	}
	slices.Sort(out)
	return out, nil
}

// ---------------------------------------------------------------- stockage, agent, connexion

// DeleteVolumeAt : le faux n'a pas de pools ; les disques sont des fichiers ordinaires.
func (f *Fake) DeleteVolumeAt(string) (bool, error)    { return false, nil }
func (f *Fake) RemovePoolsAt(string) ([]string, error) { return nil, nil }
func (f *Fake) AgentReady(string) bool                 { return false }
func (f *Fake) AgentCommand(name, _ string, _ time.Duration) (string, error) {
	return "", fmt.Errorf("agent invité de %s : absent du faux hyperviseur", name)
}
func (f *Fake) AgentExec(name, _ string) (string, int, error) {
	return "", -1, fmt.Errorf("agent invité de %s : absent du faux hyperviseur", name)
}
func (f *Fake) Close() {}
