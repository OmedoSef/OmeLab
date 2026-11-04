// Package virt parle à libvirt (qemu:///system) directement par sa socket, en Go pur : domaines, adresses IP,
// snapshots, réseaux. Il remplace les appels à virsh.
//
// La connexion est ouverte au premier usage et rétablie automatiquement si libvirtd l'a fermée
// (libvirtd se met en veille après une période d'inactivité).
package virt

import (
	"encoding/xml"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/digitalocean/go-libvirt"
	"github.com/digitalocean/go-libvirt/socket/dialers"
)

// ErrNotFound : domaine, réseau ou snapshot inexistant.
var ErrNotFound = errors.New("introuvable")

// Conn est une connexion à libvirt, sûre pour un usage concurrent.
type Conn struct {
	mu sync.Mutex
	lv *libvirt.Libvirt
}

var (
	shared     *Conn
	sharedOnce sync.Once
)

// Shared renvoie la connexion commune au processus (ouverte au premier usage).
func Shared() *Conn {
	sharedOnce.Do(func() { shared = &Conn{} })
	return shared
}

// client renvoie une connexion ouverte, en la (r)établissant si besoin.
func (c *Conn) client() (*libvirt.Libvirt, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.lv != nil && c.lv.IsConnected() {
		return c.lv, nil
	}
	lv := libvirt.NewWithDialer(dialers.NewLocal(dialers.WithLocalTimeout(5 * time.Second)))
	if err := lv.ConnectToURI(libvirt.QEMUSystem); err != nil {
		return nil, fmt.Errorf("connexion à libvirt (qemu:///system) impossible : %w (libvirtd actif ? utilisateur dans le groupe libvirt ?)", err)
	}
	c.lv = lv
	return lv, nil
}

// do exécute f avec une connexion ; si la connexion était tombée, une seconde tentative est faite.
func (c *Conn) do(f func(*libvirt.Libvirt) error) error {
	lv, err := c.client()
	if err != nil {
		return err
	}
	err = f(lv)
	if err != nil && !lv.IsConnected() {
		if lv, err2 := c.client(); err2 == nil {
			return f(lv)
		}
	}
	return err
}

// Close ferme la connexion.
func (c *Conn) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.lv != nil {
		_ = c.lv.Disconnect()
		c.lv = nil
	}
}

func notFound(err error) bool {
	var code uint32
	var le libvirt.Error
	var lp *libvirt.Error
	switch {
	case errors.As(err, &le):
		code = le.Code
	case errors.As(err, &lp):
		code = lp.Code
	default:
		return false
	}
	return code == uint32(libvirt.ErrNoDomain) || code == uint32(libvirt.ErrNoNetwork) || code == uint32(libvirt.ErrNoDomainSnapshot)
}

// ---------------------------------------------------------------- domaines

// Domain résume une VM.
type Domain struct {
	Name        string
	State       string // running, shut off, paused... (mêmes libellés que virsh)
	Description string
}

var stateNames = map[libvirt.DomainState]string{
	libvirt.DomainNostate: "no state", libvirt.DomainRunning: "running", libvirt.DomainBlocked: "idle",
	libvirt.DomainPaused: "paused", libvirt.DomainShutdown: "in shutdown", libvirt.DomainShutoff: "shut off",
	libvirt.DomainCrashed: "crashed", libvirt.DomainPmsuspended: "pmsuspended",
}

func (c *Conn) lookup(lv *libvirt.Libvirt, name string) (libvirt.Domain, error) {
	d, err := lv.DomainLookupByName(name)
	if notFound(err) {
		return d, fmt.Errorf("VM %s %w", name, ErrNotFound)
	}
	return d, err
}

// State renvoie l'état d'une VM, ou "absente".
func (c *Conn) State(name string) string {
	state := "absente"
	_ = c.do(func(lv *libvirt.Libvirt) error {
		d, err := c.lookup(lv, name)
		if err != nil {
			return err
		}
		s, _, err := lv.DomainGetState(d, 0)
		if err == nil {
			state = stateNames[libvirt.DomainState(s)]
		}
		return err
	})
	return state
}

// Domains liste toutes les VMs (actives ou non), triées par nom.
func (c *Conn) Domains() ([]Domain, error) {
	var out []Domain
	err := c.do(func(lv *libvirt.Libvirt) error {
		out = nil
		doms, _, err := lv.ConnectListAllDomains(1, 0)
		if err != nil {
			return err
		}
		for _, d := range doms {
			s, _, err := lv.DomainGetState(d, 0)
			if err != nil {
				continue // VM supprimée entre-temps
			}
			desc, _ := lv.DomainGetMetadata(d, int32(libvirt.DomainMetadataDescription), nil, 0)
			out = append(out, Domain{Name: d.Name, State: stateNames[libvirt.DomainState(s)], Description: desc})
		}
		return nil
	})
	sortBy(out, func(d Domain) string { return d.Name })
	return out, err
}

// Interface est une carte réseau d'une VM (définition libvirt).
type Interface struct {
	Type    string // network, bridge...
	Network string // réseau libvirt (type network)
	MAC     string
	Model   string
}

type domainXML struct {
	Interfaces []struct {
		Type string `xml:"type,attr"`
		MAC  struct {
			Address string `xml:"address,attr"`
		} `xml:"mac"`
		Source struct {
			Network string `xml:"network,attr"`
			Bridge  string `xml:"bridge,attr"`
		} `xml:"source"`
		Model struct {
			Type string `xml:"type,attr"`
		} `xml:"model"`
	} `xml:"devices>interface"`
}

// Interfaces renvoie les cartes réseau d'une VM (configuration en cours).
func (c *Conn) Interfaces(name string) ([]Interface, error) {
	var out []Interface
	err := c.do(func(lv *libvirt.Libvirt) error {
		d, err := c.lookup(lv, name)
		if err != nil {
			return err
		}
		x, err := lv.DomainGetXMLDesc(d, 0)
		if err != nil {
			return err
		}
		var dx domainXML
		if err := xml.Unmarshal([]byte(x), &dx); err != nil {
			return err
		}
		out = nil
		for _, i := range dx.Interfaces {
			out = append(out, Interface{Type: i.Type, Network: i.Source.Network, MAC: strings.ToLower(i.MAC.Address), Model: i.Model.Type})
		}
		return nil
	})
	return out, err
}

// Address est une adresse IPv4 vue par libvirt.
type Address struct {
	Iface string
	MAC   string
	IP    string
}

// Sources d'adresses, dans l'ordre essayé par IPv4 : bail DHCP libvirt, agent invité, table ARP.
const (
	SourceLease = uint32(libvirt.DomainInterfaceAddressesSrcLease)
	SourceAgent = uint32(libvirt.DomainInterfaceAddressesSrcAgent)
	SourceARP   = uint32(2)
)

// Addresses renvoie les adresses IPv4 (hors boucle locale) d'une VM pour une source donnée.
func (c *Conn) Addresses(name string, source uint32) ([]Address, error) {
	var out []Address
	err := c.do(func(lv *libvirt.Libvirt) error {
		d, err := c.lookup(lv, name)
		if err != nil {
			return err
		}
		ifs, err := lv.DomainInterfaceAddresses(d, source, 0)
		if err != nil {
			return err
		}
		out = nil
		for _, i := range ifs {
			mac := ""
			if len(i.Hwaddr) > 0 {
				mac = strings.ToLower(i.Hwaddr[0])
			}
			for _, a := range i.Addrs {
				if a.Type == 0 && i.Name != "lo" && !strings.HasPrefix(a.Addr, "127.") { // 0 = IPv4
					out = append(out, Address{Iface: i.Name, MAC: mac, IP: a.Addr})
				}
			}
		}
		return nil
	})
	return out, err
}

// IPv4 renvoie la première IPv4 trouvée par bail DHCP libvirt, agent invité puis ARP ; "" si aucune.
func (c *Conn) IPv4(name string) string {
	for _, src := range []uint32{SourceLease, SourceAgent, SourceARP} {
		if a, err := c.Addresses(name, src); err == nil && len(a) > 0 {
			return a[0].IP
		}
	}
	return ""
}

// ---------------------------------------------------------------- snapshots

// Snapshot décrit un snapshot de VM.
type Snapshot struct {
	Name    string
	Created time.Time
	State   string // état de la VM au moment du snapshot
	Desc    string
}

type snapshotXML struct {
	Name         string `xml:"name"`
	Description  string `xml:"description"`
	State        string `xml:"state"`
	CreationTime int64  `xml:"creationTime"`
}

// Snapshots liste les snapshots d'une VM, du plus ancien au plus récent.
func (c *Conn) Snapshots(name string) ([]Snapshot, error) {
	var out []Snapshot
	err := c.do(func(lv *libvirt.Libvirt) error {
		d, err := c.lookup(lv, name)
		if err != nil {
			return err
		}
		snaps, _, err := lv.DomainListAllSnapshots(d, 1, 0)
		if err != nil {
			return err
		}
		out = nil
		for _, s := range snaps {
			x, err := lv.DomainSnapshotGetXMLDesc(s, 0)
			if err != nil {
				continue
			}
			var sx snapshotXML
			if xml.Unmarshal([]byte(x), &sx) != nil {
				continue
			}
			out = append(out, Snapshot{Name: sx.Name, Created: time.Unix(sx.CreationTime, 0), State: sx.State, Desc: sx.Description})
		}
		return nil
	})
	sortBy(out, func(s Snapshot) string { return s.Created.Format(time.RFC3339Nano) + s.Name })
	return out, err
}

// SnapshotNames renvoie les noms des snapshots d'une VM (vide si la VM n'existe pas).
func (c *Conn) SnapshotNames(name string) []string {
	snaps, _ := c.Snapshots(name)
	out := make([]string, len(snaps))
	for i, s := range snaps {
		out[i] = s.Name
	}
	return out
}

// CreateSnapshot prend un snapshot (interne qcow2, VM allumée ou éteinte), de façon atomique.
func (c *Conn) CreateSnapshot(name, snap, desc string) error {
	var buf strings.Builder
	buf.WriteString("<domainsnapshot><name>")
	_ = xml.EscapeText(&buf, []byte(snap))
	buf.WriteString("</name>")
	if desc != "" {
		buf.WriteString("<description>")
		_ = xml.EscapeText(&buf, []byte(desc))
		buf.WriteString("</description>")
	}
	buf.WriteString("</domainsnapshot>")
	return c.do(func(lv *libvirt.Libvirt) error {
		d, err := c.lookup(lv, name)
		if err != nil {
			return err
		}
		_, err = lv.DomainSnapshotCreateXML(d, buf.String(), uint32(libvirt.DomainSnapshotCreateAtomic))
		return err
	})
}

func (c *Conn) snapshot(lv *libvirt.Libvirt, name, snap string) (libvirt.DomainSnapshot, error) {
	d, err := c.lookup(lv, name)
	if err != nil {
		return libvirt.DomainSnapshot{}, err
	}
	s, err := lv.DomainSnapshotLookupByName(d, snap, 0)
	if notFound(err) {
		return s, fmt.Errorf("snapshot %s de %s %w", snap, name, ErrNotFound)
	}
	return s, err
}

// RevertSnapshot restaure une VM à un snapshot (elle reprend l'état qu'elle avait : allumée ou éteinte).
func (c *Conn) RevertSnapshot(name, snap string) error {
	return c.do(func(lv *libvirt.Libvirt) error {
		s, err := c.snapshot(lv, name, snap)
		if err != nil {
			return err
		}
		return lv.DomainRevertToSnapshot(s, 0)
	})
}

// DeleteSnapshot supprime un snapshot.
func (c *Conn) DeleteSnapshot(name, snap string) error {
	return c.do(func(lv *libvirt.Libvirt) error {
		s, err := c.snapshot(lv, name, snap)
		if err != nil {
			return err
		}
		return lv.DomainSnapshotDelete(s, 0)
	})
}
