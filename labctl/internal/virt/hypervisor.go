package virt

import "time"

// Hypervisor est ce que labctl demande à libvirt. *Conn l'implémente sur la vraie socket ; les tests du moteur
// (paquet lab) lui substituent un faux en mémoire (virt/virtfake). Les méthodes ont la sémantique de *Conn :
// State renvoie StateAbsent pour une VM inconnue, les autres signalent ErrNotFound (errors.Is).
type Hypervisor interface {
	// Domaines
	State(name string) string
	Domains() ([]Domain, error)
	Description(name string) string
	DomainXML(name string, inactive bool) (string, error)
	DefineDomain(xmlDesc string) error
	StartDomain(name string) error
	ShutdownDomain(name string) error
	RebootDomain(name string) error
	DestroyDomain(name string) error
	UndefineDomain(name string) error
	IsEFI(name string) bool
	Resources(name string) (cpus, memMiB int, err error)
	SetResources(name string, cpus, memMiB int) error
	Disks(name string) ([]string, error)
	RemoveCDROMs(name string) error
	SendKey(name string, code uint32) error

	// Cartes réseau et adresses
	Interfaces(name string) ([]Interface, error)
	AttachInterface(name, network, mac, model string, live bool) error
	DetachInterface(name, mac string, live bool) error
	Addresses(name string, source uint32) ([]Address, error)
	IPv4(name string) string

	// Snapshots
	Snapshots(name string) ([]Snapshot, error)
	SnapshotNames(name string) []string
	CreateSnapshot(name, snap, desc string) error
	RevertSnapshot(name, snap string) error
	DeleteSnapshot(name, snap string) error

	// Réseaux
	Networks() ([]Network, error)
	NetExists(name string) bool
	NetActive(name string) bool
	NetXML(name string, inactive bool) (string, error)
	DefineNetwork(xmlDesc string) error
	StartNetwork(name string) error
	DeleteNetwork(name string) error
	NetworkUsers(name string) ([]string, error)

	// Stockage
	DeleteVolumeAt(path string) (bool, error)
	RemovePoolsAt(dir string) ([]string, error)

	// Agent invité (qemu-guest-agent)
	AgentReady(name string) bool
	AgentCommand(name, cmd string, timeout time.Duration) (string, error)
	AgentExec(name, script string) (string, int, error)

	// Connexion
	Close()
}

var _ Hypervisor = (*Conn)(nil)
