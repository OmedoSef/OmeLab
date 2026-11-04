// Package coreconf génère la configuration de la passerelle core-gw à partir de la description des
// modules : adresse .254 de la passerelle sur chaque réseau (systemd-networkd), DHCP commun, deux
// instances DNS par module (open : relais Internet ; closed : noms du lab seulement), liens entre
// modules et VMs bloquées pour le pare-feu (lus par gwctl).
//
// Fonctions pures : la description en entrée, le contenu des fichiers en sortie, sans accès système.
package coreconf

import (
	"fmt"
	"net/netip"
	"sort"
	"strings"
)

const (
	GatewayOctet   = 254
	DHCPRangeStart = 100
	DHCPRangeEnd   = 199
	firstDNSPort   = 5300
)

// Module décrit un module tel que la passerelle le voit.
type Module struct {
	Name       string
	Subnet     string            // trois premiers octets, ex. 10.66.66
	Domain     string            // domaine DNS du module
	Hosts      map[string]int    // VM -> dernier octet (module.env + réservations de core.conf)
	MACs       map[string]string // VM -> MAC (VMs sans MAC : DNS seulement, pas de réservation DHCP)
	NoInternet []string          // VMs bloquées comme source (Internet et modules liés)
	Aliases    map[string]string // alias -> cible (VM du module ou nom complet)
	Links      []string          // modules que celui-ci peut joindre et résoudre
}

// Input est l'ensemble de ce qu'il faut pour générer la configuration.
type Input struct {
	Modules    []Module // tous les modules qui ont un réseau, dans l'ordre (par nom)
	Attached   []string // modules dont la passerelle est branchée sur le réseau
	Upstream   string   // DNS amont pour les instances open (passerelle du NAT libvirt)
	RootDomain string   // domaine racine des labs, jamais relayé vers Internet
}

// Files associe un chemin relatif (gwdns/..., networkd/...) à son contenu.
type Files map[string]string

// GatewayMAC renvoie la MAC de la carte de la passerelle sur un sous-réseau : 52:54:00:fe:<2e>:<3e>.
func GatewayMAC(subnet string) string {
	var a, b, c int
	fmt.Sscanf(subnet, "%d.%d.%d", &a, &b, &c)
	return fmt.Sprintf("52:54:00:fe:%02x:%02x", b, c)
}

// IP renvoie l'adresse réservée d'une VM du module.
func (m *Module) IP(vm string) string { return fmt.Sprintf("%s.%d", m.Subnet, m.Hosts[vm]) }

// vmsByIP renvoie les VMs triées par adresse (sortie stable).
func (m *Module) vmsByIP() []string {
	vms := make([]string, 0, len(m.Hosts))
	for vm := range m.Hosts {
		vms = append(vms, vm)
	}
	sort.Slice(vms, func(i, j int) bool {
		if m.Hosts[vms[i]] != m.Hosts[vms[j]] {
			return m.Hosts[vms[i]] < m.Hosts[vms[j]]
		}
		return vms[i] < vms[j]
	})
	return vms
}

// Render produit tous les fichiers de configuration.
func Render(in Input) (Files, error) {
	byName := map[string]*Module{}
	for i := range in.Modules {
		m := &in.Modules[i]
		if _, err := netip.ParseAddr(m.Subnet + ".0"); err != nil {
			return nil, fmt.Errorf("module %s : sous-réseau invalide %q", m.Name, m.Subnet)
		}
		byName[m.Name] = m
	}
	attached := map[string]bool{}
	var order []*Module // modules branchés, dans l'ordre des modules
	for _, m := range in.Modules {
		for _, a := range in.Attached {
			if a == m.Name {
				attached[m.Name] = true
				order = append(order, byName[m.Name])
			}
		}
	}
	if len(order) > 0 && in.Upstream == "" {
		return nil, fmt.Errorf("DNS amont inconnu (réseau libvirt default absent ?)")
	}

	f := Files{}
	var modules, links, blocked strings.Builder
	for i, m := range order {
		closed := firstDNSPort + 2*i
		fmt.Fprintf(&modules, "%s.0/24 %d %d %s %s\n", m.Subnet, closed, closed+1, m.Name, m.Domain)
		f["networkd/50-gw-"+m.Name+".network"] = networkd(m)
		f["gwdns/"+m.Name+"-closed.conf"] = dns(in, m, closed, false, attached)
		f["gwdns/"+m.Name+"-open.conf"] = dns(in, m, closed+1, true, attached)
		for _, other := range m.Links {
			if o, ok := byName[other]; ok && attached[other] {
				fmt.Fprintf(&links, "%s.0/24 %s.0/24\n", m.Subnet, o.Subnet)
			}
		}
		for _, vm := range m.NoInternet {
			if _, ok := m.Hosts[vm]; !ok {
				return nil, fmt.Errorf("module %s : NO_INTERNET cite %s, absente de HOSTS", m.Name, vm)
			}
			fmt.Fprintln(&blocked, m.IP(vm))
		}
	}
	f["gwdns/modules"] = modules.String()
	f["gwdns/links"] = links.String()
	f["gwdns/blocked"] = blocked.String()
	f["gwdns/dhcp.conf"] = dhcp(order)
	return f, nil
}

// networkd : adresse fixe de la passerelle sur le réseau du module, carte reconnue par sa MAC.
func networkd(m *Module) string {
	return fmt.Sprintf(`# Généré par labctl core-config : carte de la passerelle sur le module %s
[Match]
MACAddress=%s

[Network]
Address=%s.%d/24
LinkLocalAddressing=no
IPv6AcceptRA=no
ConfigureWithoutCarrier=yes
`, m.Name, GatewayMAC(m.Subnet), m.Subnet, GatewayOctet)
}

// dhcp : une plage et des réservations par module branché, repérées par un tag.
func dhcp(order []*Module) string {
	var b strings.Builder
	b.WriteString(`# Généré par labctl core-config : DHCP des modules
port=0
bind-dynamic
except-interface=eth0
except-interface=lo
dhcp-authoritative
dhcp-leasefile=/var/lib/misc/gwdhcp.leases
dhcp-script=/usr/local/sbin/gwdhcp-hook
dhcp-scriptuser=root
`)
	for _, m := range order {
		gw := fmt.Sprintf("%s.%d", m.Subnet, GatewayOctet)
		fmt.Fprintf(&b, "\n# module %s (%s)\n", m.Name, m.Domain)
		fmt.Fprintf(&b, "dhcp-range=set:%s,%s.%d,%s.%d,255.255.255.0,12h\n", m.Name, m.Subnet, DHCPRangeStart, m.Subnet, DHCPRangeEnd)
		fmt.Fprintf(&b, "dhcp-option=tag:%s,option:router,%s\n", m.Name, gw)
		fmt.Fprintf(&b, "dhcp-option=tag:%s,option:dns-server,%s\n", m.Name, gw)
		fmt.Fprintf(&b, "dhcp-option=tag:%s,option:domain-name,%s\n", m.Name, m.Domain)
		fmt.Fprintf(&b, "dhcp-option=tag:%s,option:domain-search,%s\n", m.Name, m.Domain)
		for _, vm := range m.vmsByIP() {
			if mac := m.MACs[vm]; mac != "" {
				fmt.Fprintf(&b, "dhcp-host=%s,%s,%s\n", mac, m.IP(vm), vm)
			}
		}
	}
	return b.String()
}

// dns : instance DNS d'un module. Il résout ses noms et ceux des modules vers lesquels il a un lien ;
// tous les domaines de modules sont locaux (inconnu = NXDOMAIN, jamais relayé vers Internet).
func dns(in Input, m *Module, port int, open bool, attached map[string]bool) string {
	mode := "closed"
	if open {
		mode = "open"
	}
	visible := map[string]bool{m.Name: true}
	for _, l := range m.Links {
		visible[l] = true
	}
	var b strings.Builder
	fmt.Fprintf(&b, `# Généré par labctl core-config : DNS du module %s, instance %s
port=%d
pid-file=/run/gwdns-%s-%s.pid
bind-dynamic
except-interface=eth0
no-resolv
no-hosts
domain-needed
bogus-priv
local-ttl=60
# Domaine racine des labs : jamais relayé vers Internet
local=/%s/
`, m.Name, mode, port, m.Name, mode, in.RootDomain)
	for i := range in.Modules {
		o := &in.Modules[i]
		fmt.Fprintf(&b, "local=/%s/\n", o.Domain)
		if !visible[o.Name] {
			fmt.Fprintf(&b, "# module %s (%s) : masqué\n", o.Name, o.Domain)
			continue
		}
		fmt.Fprintf(&b, "# module %s (%s) : visible\n", o.Name, o.Domain)
		for _, vm := range o.vmsByIP() {
			fmt.Fprintf(&b, "host-record=%s.%s,%s\n", vm, o.Domain, o.IP(vm))
		}
		aliases := make([]string, 0, len(o.Aliases))
		for a := range o.Aliases {
			aliases = append(aliases, a)
		}
		sort.Strings(aliases)
		for _, a := range aliases {
			target := o.Aliases[a]
			if !strings.Contains(target, ".") {
				target += "." + o.Domain
			}
			fmt.Fprintf(&b, "cname=%s.%s,%s\n", a, o.Domain, target)
		}
		// Noms obtenus par DHCP (tenus à jour par gwdhcp-hook)
		if attached[o.Name] {
			fmt.Fprintf(&b, "hostsdir=/run/gwdns/leases/%s\n", o.Name)
		}
	}
	if open {
		fmt.Fprintf(&b, "# Relais Internet (VMs autorisées par la passerelle)\nserver=%s\n", in.Upstream)
	}
	return b.String()
}
