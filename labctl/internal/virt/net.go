package virt

import (
	"encoding/xml"
	"fmt"
	"net/netip"
	"sort"
	"strings"

	"github.com/digitalocean/go-libvirt"
)

// Network résume un réseau libvirt.
type Network struct {
	Name      string
	Active    bool
	Autostart bool
	Bridge    string
	Forward   string // nat, route... ; "" = isolé
	HostIP    string // adresse de l'hôte sur le réseau
	Prefix    int    // longueur du préfixe (24...)
	Domain    string
	DHCP      bool
}

// NetworkXML est la partie de la définition d'un réseau que lit labctl.
type NetworkXML struct {
	Name    string `xml:"name"`
	Forward struct {
		Mode string `xml:"mode,attr"`
	} `xml:"forward"`
	Bridge struct {
		Name string `xml:"name,attr"`
	} `xml:"bridge"`
	Domain struct {
		Name string `xml:"name,attr"`
	} `xml:"domain"`
	IPs []struct {
		Address string `xml:"address,attr"`
		Netmask string `xml:"netmask,attr"`
		Prefix  int    `xml:"prefix,attr"`
		DHCP    *struct {
			Ranges []struct {
				Start string `xml:"start,attr"`
			} `xml:"range"`
		} `xml:"dhcp"`
	} `xml:"ip"`
}

func (c *Conn) lookupNet(lv *libvirt.Libvirt, name string) (libvirt.Network, error) {
	n, err := lv.NetworkLookupByName(name)
	if notFound(err) {
		return n, fmt.Errorf("réseau %s %w", name, ErrNotFound)
	}
	return n, err
}

// NetExists indique si un réseau est défini.
func (c *Conn) NetExists(name string) bool {
	return c.do(func(lv *libvirt.Libvirt) error { _, err := c.lookupNet(lv, name); return err }) == nil
}

// NetActive indique si un réseau est démarré.
func (c *Conn) NetActive(name string) bool {
	active := false
	_ = c.do(func(lv *libvirt.Libvirt) error {
		n, err := c.lookupNet(lv, name)
		if err != nil {
			return err
		}
		a, err := lv.NetworkIsActive(n)
		active = a == 1
		return err
	})
	return active
}

// NetXML renvoie la définition d'un réseau (inactive = configuration enregistrée plutôt qu'en service).
func (c *Conn) NetXML(name string, inactive bool) (string, error) {
	var x string
	err := c.do(func(lv *libvirt.Libvirt) error {
		n, err := c.lookupNet(lv, name)
		if err != nil {
			return err
		}
		flags := uint32(0)
		if inactive {
			flags = uint32(libvirt.NetworkXMLInactive)
		}
		x, err = lv.NetworkGetXMLDesc(n, flags)
		return err
	})
	return x, err
}

// Networks liste les réseaux libvirt, triés par nom.
func (c *Conn) Networks() ([]Network, error) {
	var out []Network
	err := c.do(func(lv *libvirt.Libvirt) error {
		nets, _, err := lv.ConnectListAllNetworks(1, 0)
		if err != nil {
			return err
		}
		out = nil
		for _, n := range nets {
			info := Network{Name: n.Name}
			a, _ := lv.NetworkIsActive(n)
			info.Active = a == 1
			auto, _ := lv.NetworkGetAutostart(n)
			info.Autostart = auto == 1
			if x, err := lv.NetworkGetXMLDesc(n, 0); err == nil {
				NetworkFromXML(&info, x)
			}
			out = append(out, info)
		}
		return nil
	})
	sortBy(out, func(n Network) string { return n.Name })
	return out, err
}

// NetworkFromXML complète info (pont, mode, domaine, adresse de l'hôte) d'après la définition x d'un réseau ;
// une définition illisible laisse info inchangé.
func NetworkFromXML(info *Network, x string) {
	var nx NetworkXML
	if xml.Unmarshal([]byte(x), &nx) != nil {
		return
	}
	info.Bridge, info.Forward, info.Domain = nx.Bridge.Name, nx.Forward.Mode, nx.Domain.Name
	if len(nx.IPs) > 0 {
		ip := nx.IPs[0]
		info.HostIP, info.Prefix, info.DHCP = ip.Address, ip.Prefix, ip.DHCP != nil
		if info.Prefix == 0 && ip.Netmask != "" {
			info.Prefix = maskBits(ip.Netmask)
		}
	}
}

// DefineNetwork enregistre (ou remplace) la définition d'un réseau.
func (c *Conn) DefineNetwork(xmlDesc string) error {
	return c.do(func(lv *libvirt.Libvirt) error { _, err := lv.NetworkDefineXML(xmlDesc); return err })
}

// StartNetwork démarre un réseau et le marque pour démarrer avec l'hôte.
func (c *Conn) StartNetwork(name string) error {
	return c.do(func(lv *libvirt.Libvirt) error {
		n, err := c.lookupNet(lv, name)
		if err != nil {
			return err
		}
		if a, _ := lv.NetworkIsActive(n); a != 1 {
			if err := lv.NetworkCreate(n); err != nil {
				return err
			}
		}
		return lv.NetworkSetAutostart(n, 1)
	})
}

// DeleteNetwork arrête et supprime la définition d'un réseau.
func (c *Conn) DeleteNetwork(name string) error {
	return c.do(func(lv *libvirt.Libvirt) error {
		n, err := c.lookupNet(lv, name)
		if err != nil {
			return err
		}
		if a, _ := lv.NetworkIsActive(n); a == 1 {
			if err := lv.NetworkDestroy(n); err != nil {
				return err
			}
		}
		return lv.NetworkUndefine(n)
	})
}

// NetworkUsers renvoie les VMs (actives ou non) qui ont une carte sur ce réseau.
func (c *Conn) NetworkUsers(name string) ([]string, error) {
	doms, err := c.Domains()
	if err != nil {
		return nil, err
	}
	var out []string
	for _, d := range doms {
		ifs, err := c.Interfaces(d.Name)
		if err != nil {
			continue
		}
		for _, i := range ifs {
			if i.Type == "network" && i.Network == name {
				out = append(out, d.Name)
				break
			}
		}
	}
	return out, nil
}

func maskBits(mask string) int {
	a, err := netip.ParseAddr(mask)
	if err != nil || !a.Is4() {
		return 0
	}
	b := a.As4()
	n := 0
	for _, x := range b {
		for ; x&0x80 != 0; x <<= 1 {
			n++
		}
	}
	return n
}

func sortBy[T any](s []T, key func(T) string) {
	sort.SliceStable(s, func(i, j int) bool { return strings.Compare(key(s[i]), key(s[j])) < 0 })
}
