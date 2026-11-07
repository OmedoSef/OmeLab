// Package coreconf génère la configuration de la passerelle core-gw à partir de la description des
// modules : adresse .254 de la passerelle sur chaque réseau (systemd-networkd), DHCP commun, deux
// instances DNS par module (open : relais Internet ; closed : noms du lab seulement), liens entre
// modules et VMs bloquées pour le pare-feu (lus par gwctl), règles des liens et connexions (insérées par gwctl
// dans le pare-feu nftables).
//
// Fonctions pures : la description en entrée, le contenu des fichiers en sortie, sans accès système.
package coreconf

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"labs/labctl/internal/module"
)

const (
	// PortalFirstPort : port du premier portail captif sur la passerelle (un port par connexion via: portal) ;
	// PortalControlPort : commande locale du service de portail (127.0.0.1 seulement).
	PortalFirstPort   = 8800
	PortalControlPort = 8799
	// ProxyPort : proxy transparent de la passerelle (mitmproxy) ; ProxyWebPort : son interface (127.0.0.1).
	ProxyPort      = 8080
	ProxyWebPort   = 8081
	GatewayOctet   = 254
	DHCPRangeStart = 100
	DHCPRangeEnd   = 199
	firstDNSPort   = 5300
)

// Module décrit un module tel que la passerelle le voit.
type Module struct {
	Name       string
	Subnet     string              // trois premiers octets, ex. 10.66.66
	Domain     string              // domaine DNS du module
	Hosts      map[string]int      // VM -> dernier octet (VMs à IP fixe + réservations dns.hosts)
	MACs       map[string]string   // VM -> MAC (VMs sans MAC : DNS seulement, pas de réservation DHCP)
	NoInternet []string            // VMs bloquées comme source (Internet et modules liés)
	Aliases    map[string]string   // alias -> cible (VM du module ou nom complet)
	Links      []string            // modules que celui-ci peut joindre et résoudre (tout, tous ports)
	Conns      []module.Connection // connexions restreintes (vers un module, une VM, ou Internet)
	NoRoute    map[string]bool     // cartes de VMs d'autres modules (attach:) : DHCP sans route ni DNS
	Sees       []string            // modules dont ce module résout les noms (ses VMs y sont branchées)
}

// Input est l'ensemble de ce qu'il faut pour générer la configuration.
type Input struct {
	Modules    []Module // tous les modules qui ont un réseau, dans l'ordre (par nom)
	Attached   []string // modules dont la passerelle est branchée sur le réseau
	Upstream   string   // DNS amont pour les instances open (passerelle du NAT libvirt)
	RootDomain string   // domaine racine des labs, jamais relayé vers Internet
	Skipped    []string // avertissements à signaler : modules écartés (Check), règles ignorées (module vulnérable)
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
	// Défense en profondeur : aucun identifiant douteux ne doit atteindre les fichiers de la passerelle, quel
	// que soit l'appelant (CoreInput écarte déjà les modules qui échouent, avec un message).
	if err := in.Check(); err != nil {
		return nil, err
	}
	byName := map[string]*Module{}
	for i := range in.Modules {
		byName[in.Modules[i].Name] = &in.Modules[i]
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
	fwMods := []string{"# Généré par labctl core-config : liens et connexions entre modules (chaîne forward)"}
	fwNet := []string{"# Généré par labctl core-config : ce que permet un accès Internet ouvert (chaîne forward)"}
	pre := []string{"# Généré par labctl core-config : portails captifs (chaîne prerouting, avant le DNS)"}
	sets := []string{"# Généré par labctl core-config : sources authentifiées de chaque portail (expiration native)"}
	portals := []PortalConf{}
	proxies := []ProxyConf{}
	api := map[string]string{} // module -> URL de l'API du portail (option DHCP 114)
	for i, m := range order {
		closed := firstDNSPort + 2*i
		fmt.Fprintf(&modules, "%s.0/24 %d %d %s %s\n", m.Subnet, closed, closed+1, m.Name, m.Domain)
		f["networkd/50-gw-"+m.Name+".network"] = networkd(m)
		f["gwdns/"+m.Name+"-closed.conf"] = dns(in, m, closed, false, attached)
		f["gwdns/"+m.Name+"-open.conf"] = dns(in, m, closed+1, true, attached)
		for _, other := range m.Links {
			if o, ok := byName[other]; ok && attached[other] {
				fmt.Fprintf(&links, "%s.0/24 %s.0/24\n", m.Subnet, o.Subnet)
				fwMods = append(fwMods, fmt.Sprintf("# lien %s -> %s (tout)", m.Name, o.Name),
					fmt.Sprintf(`oifname != "eth0" ip saddr %s.0/24 ip daddr %s.0/24 accept`, m.Subnet, o.Subnet))
			}
		}
		for _, cn := range m.Conns {
			r, err := connRules(m, cn, byName, attached, len(portals), closed+1)
			if err != nil {
				return nil, fmt.Errorf("module %s, connexion vers %s : %w", m.Name, cn.To, err)
			}
			fwMods, fwNet, pre, sets = append(fwMods, r.mods...), append(fwNet, r.internet...), append(pre, r.pre...), append(sets, r.sets...)
			if r.proxy != nil {
				r.proxy.ID = len(proxies)
				proxies = append(proxies, *r.proxy)
			}
			if r.portal != nil {
				portals = append(portals, *r.portal)
				if r.portal.API && api[m.Name] == "" {
					api[m.Name] = fmt.Sprintf("http://%s:%d/api", r.portal.Gateway, r.portal.Port)
				}
			}
		}
		for _, vm := range m.NoInternet {
			if _, ok := m.Hosts[vm]; !ok {
				return nil, fmt.Errorf("module %s : VM « internet: never » %s sans IP fixe", m.Name, vm)
			}
			fmt.Fprintln(&blocked, m.IP(vm))
		}
	}
	f["gwdns/modules"] = modules.String()
	f["gwdns/links"] = links.String()
	f["gwdns/forward-modules.nft"] = strings.Join(fwMods, "\n") + "\n"
	f["gwdns/forward-internet.nft"] = strings.Join(fwNet, "\n") + "\n"
	f["gwdns/blocked"] = blocked.String()
	f["gwdns/prerouting.nft"] = strings.Join(pre, "\n") + "\n"
	f["gwdns/sets.nft"] = strings.Join(sets, "\n") + "\n"
	pj, _ := json.MarshalIndent(portals, "", "  ")
	f["gwdns/portal.json"] = string(pj) + "\n"
	xj, _ := json.MarshalIndent(proxies, "", "  ")
	f["gwdns/proxy.json"] = string(xj) + "\n"
	f["gwdns/dhcp.conf"] = dhcp(order, api)
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
func dhcp(order []*Module, api map[string]string) string {
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
		// Carte d'une VM branchée depuis un autre module (tag attached) : ni route par défaut ni DNS, sa carte
		// principale (dans son module) les fournit déjà
		fmt.Fprintf(&b, "dhcp-option=tag:%s,tag:!attached,option:router,%s\n", m.Name, gw)
		fmt.Fprintf(&b, "dhcp-option=tag:%s,tag:!attached,option:dns-server,%s\n", m.Name, gw)
		// sans valeur : option non envoyée (sinon dnsmasq annonce d'office sa propre adresse comme routeur et DNS)
		fmt.Fprintf(&b, "dhcp-option=tag:%s,tag:attached,option:router\n", m.Name)
		fmt.Fprintf(&b, "dhcp-option=tag:%s,tag:attached,option:dns-server\n", m.Name)
		fmt.Fprintf(&b, "dhcp-option=tag:%s,option:domain-name,%s\n", m.Name, m.Domain)
		fmt.Fprintf(&b, "dhcp-option=tag:%s,option:domain-search,%s\n", m.Name, m.Domain)
		if u := api[m.Name]; u != "" { // RFC 8910 : annonce du portail captif
			fmt.Fprintf(&b, "dhcp-option-force=tag:%s,114,\"%s\"\n", m.Name, u)
		}
		for _, vm := range m.vmsByIP() {
			if mac := m.MACs[vm]; mac != "" && m.NoRoute[vm] {
				fmt.Fprintf(&b, "dhcp-host=%s,set:attached,%s,%s\n", mac, m.IP(vm), vm)
			} else if mac != "" {
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
	for _, l := range m.Sees {
		visible[l] = true
	}
	for _, cn := range m.Conns {
		if mod, _ := cn.Target(); mod != "" {
			visible[mod] = true
		}
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

// PortalConf : un portail captif, tel que le service de portail de la passerelle le lit (gwdns/portal.json).
type PortalConf struct {
	ID       int      `json:"id"`
	Port     int      `json:"port"`    // port d'écoute (redirection du HTTP non authentifié)
	Set      string   `json:"set"`     // ensemble nftables des sources authentifiées
	Module   string   `json:"module"`  // module des sources
	Gateway  string   `json:"gateway"` // adresse de la passerelle sur ce module
	Sources  []string `json:"sources"` // IP ou sous-réseau des sources concernées
	Target   string   `json:"target"`  // internet, module ou module/vm
	Auth     string   `json:"auth"`
	Password string   `json:"password,omitempty"`
	Title    string   `json:"title"`
	Seconds  int      `json:"seconds"` // durée d'une session
	Detect   bool     `json:"detect"`
	API      bool     `json:"api"`
}

// ProxyConf : une connexion via le proxy, telle que le module mitmproxy de la passerelle la lit
// (gwdns/proxy.json) pour choisir le mode selon la source et la destination.
type ProxyConf struct {
	ID      int      `json:"id"`
	Module  string   `json:"module"`
	Sources []string `json:"sources"` // IP ou sous-réseau des sources
	Target  string   `json:"target"`  // internet, module ou module/vm
	Dest    string   `json:"dest"`    // internet ou sous-réseau / IP de la cible
	Mode    string   `json:"mode"`    // log | filter | intercept
	Allow   []string `json:"allow,omitempty"`
	Deny    []string `json:"deny,omitempty"`
}

// rules : ce qu'une connexion ajoute à chaque fragment de pare-feu.
type rules struct {
	mods, internet, pre, sets []string
	portal                    *PortalConf
	proxy                     *ProxyConf
}

// connRules traduit une connexion en règles nftables. Une connexion est un OCTROI : elle ouvre ses ports
// pour ses sources, qu'Internet soit ouvert à la main (core internet on) ou non — comme un lien entre
// modules. Pour Internet, on ajoute un refus du reste pour ces sources (sans quoi la règle générale
// « autorisé : tout Internet » les laisserait passer quand l'ouverture manuelle est active) et une
// redirection de leur DNS vers le résolveur « open » du module (résolution des noms publics).
//
// Derrière un portail (via: portal, numéro id), l'acceptation demande en plus d'être dans l'ensemble
// portal_<id> (sources authentifiées, retirées à l'expiration) ; avant, le HTTP vers la cible est redirigé
// vers le portail (pre). Un portail garde l'accès derrière l'authentification.
func connRules(m *Module, cn module.Connection, byName map[string]*Module, attached map[string]bool, id, openDNS int) (r rules, err error) {
	src := m.Subnet + ".0/24"
	sources := []string{src}
	if len(cn.From) > 0 {
		ips := make([]string, 0, len(cn.From))
		for _, f := range cn.From {
			if _, ok := m.Hosts[f]; !ok {
				return r, fmt.Errorf("source %s sans IP fixe", f)
			}
			ips = append(ips, m.IP(f))
		}
		src, sources = "{ "+strings.Join(ips, ", ")+" }", ips
	}
	matches, err := portMatches(cn.Ports)
	if err != nil {
		return r, err
	}
	desc := fmt.Sprintf("# connexion %s%s -> %s%s", m.Name, fromDesc(cn.From), cn.To, portsDesc(cn.Ports))

	// Cible : Internet (hors réseaux privés) ou un module / une VM
	dst := "!= @private"
	if cn.To != module.Internet {
		mod, vm := cn.Target()
		o, ok := byName[mod]
		if !ok {
			return r, fmt.Errorf("module inconnu")
		}
		if !attached[mod] {
			r.mods = []string{desc + " : module non branché, ignorée"}
			return r, nil
		}
		dst = o.Subnet + ".0/24"
		if vm != "" {
			if _, ok := o.Hosts[vm]; !ok {
				return r, fmt.Errorf("%s n'a pas d'IP fixe dans %s", vm, mod)
			}
			dst = o.IP(vm)
		}
	}

	// Une connexion est un octroi explicite : elle ouvre ses ports sans dépendre de l'ouverture manuelle
	// (core internet on), exactement comme un lien ou une connexion entre modules. Les sources bloquées
	// sont déjà rejetées en amont de la chaîne forward (ip saddr @blocked reject).
	gate := ""
	if cn.Via == "portal" {
		pt := cn.Portal
		pc := &PortalConf{ID: id, Port: PortalFirstPort + id, Set: fmt.Sprintf("portal_%d", id), Module: m.Name,
			Gateway: fmt.Sprintf("%s.%d", m.Subnet, GatewayOctet), Sources: sources, Target: cn.To,
			Auth: "click", Title: "Portail du lab", Seconds: int(pt.Session().Seconds()), Detect: pt.Detects()}
		if pt != nil {
			pc.Password, pc.API = pt.Password, pt.API
			if pt.Auth != "" {
				pc.Auth = pt.Auth
			}
			if pt.Title != "" {
				pc.Title = pt.Title
			}
		}
		r.portal = pc
		desc += fmt.Sprintf(" via portail %d (port %d, %s)", id, pc.Port, pc.Auth)
		gate = fmt.Sprintf("ip saddr @%s ", pc.Set)
		r.sets = append(r.sets, fmt.Sprintf("set %s { type ipv4_addr; flags timeout; }", pc.Set))
		r.pre = append(r.pre, desc,
			fmt.Sprintf("ip saddr %s ip saddr != @%s ip daddr %s tcp dport 80 redirect to :%d", src, pc.Set, dst, pc.Port))
		if cn.To == module.Internet {
			r.pre = append(r.pre, fmt.Sprintf("ip saddr %s ip saddr != @blocked meta l4proto { tcp, udp } th dport 53 redirect to :%d", src, openDNS))
		}
	} else if cn.To == module.Internet {
		// direct ou proxy vers Internet : la source obtient la résolution externe (instance DNS « open » du
		// module), sinon elle tomberait sur le résolveur « closed » (noms du lab seulement) tant qu'Internet
		// n'est pas ouvert à la main — et sa connexion ne pourrait résoudre aucun nom public.
		r.pre = append(r.pre, fmt.Sprintf("# connexion %s%s -> internet : DNS externe", m.Name, fromDesc(cn.From)),
			fmt.Sprintf("ip saddr %s ip saddr != @blocked meta l4proto { tcp, udp } th dport 53 redirect to :%d", src, openDNS))
	}
	if cn.Via == "proxy" {
		// HTTP(S) vers la cible redirigé vers le proxy (qui se connecte lui-même à la vraie destination).
		// L'octroi vaut Internet ouvert ou non. Les VMs bloquées ne passent pas (rejetées en amont).
		px := cn.Proxy
		mode := "log"
		if px != nil && px.Mode != "" {
			mode = px.Mode
		}
		pc := &ProxyConf{Module: m.Name, Sources: sources, Target: cn.To, Dest: "internet", Mode: mode}
		if cn.To != module.Internet {
			pc.Dest = dst
		}
		if px != nil {
			pc.Allow, pc.Deny = px.Allow, px.Deny
		}
		r.proxy = pc
		var pp []string
		for _, p := range px.ProxyPorts() {
			if p.Lo == p.Hi {
				pp = append(pp, fmt.Sprint(p.Lo))
			} else {
				pp = append(pp, fmt.Sprintf("%d-%d", p.Lo, p.Hi))
			}
		}
		desc += fmt.Sprintf(" via proxy (%s)", mode)
		r.pre = append(r.pre, desc, fmt.Sprintf("ip saddr %s ip saddr != @blocked %sip daddr %s tcp dport { %s } redirect to :%d",
			src, gate, dst, strings.Join(pp, ", "), ProxyPort))
	}

	// Proxy : le HTTP(S) part vers mitmproxy par la redirection ci-dessus ; les ports: éventuels passent en
	// direct, mais SANS ports: on n'ouvre rien d'autre. En particulier on ne laisse pas QUIC (UDP/443) filer
	// en direct : coupé, les navigateurs retombent sur TCP/443 — visible par le proxy (un proxy ouvert à tout
	// serait aveugle au HTTP/3). Pour autoriser QUIC quand même, mettre udp/443 dans ports:.
	if cn.Via == "proxy" && len(cn.Ports) == 0 {
		matches = nil
	}
	var out []string
	for _, pm := range matches {
		if cn.To == module.Internet {
			out = append(out, fmt.Sprintf("ip saddr %s %sip daddr %s %saccept", src, gate, dst, pm))
		} else {
			out = append(out, fmt.Sprintf(`oifname != "eth0" ip saddr %s %sip daddr %s %saccept`, src, gate, dst, pm))
		}
	}
	if cn.Via == "portal" && cn.Portal != nil && cn.Portal.HTTPS == "drop" { // HTTPS non authentifié : sans réponse
		if cn.To == module.Internet {
			out = append(out, fmt.Sprintf("ip saddr %s ip daddr %s tcp dport 443 drop", src, dst))
		} else {
			out = append(out, fmt.Sprintf(`oifname != "eth0" ip saddr %s ip daddr %s tcp dport 443 drop`, src, dst))
		}
	}
	if cn.To == module.Internet {
		r.internet = append(append([]string{desc}, out...), fmt.Sprintf("ip saddr %s reject with icmpx admin-prohibited", src))
	} else {
		r.mods = append([]string{desc}, out...)
	}
	return r, nil
}

// portMatches regroupe les ports par protocole : une condition nftables par protocole (« » = tout).
func portMatches(ports []string) ([]string, error) {
	if len(ports) == 0 {
		return []string{""}, nil
	}
	by := map[string][]string{}
	icmp := false
	for _, s := range ports {
		p, err := module.ParsePort(s)
		if err != nil {
			return nil, err
		}
		switch {
		case p.Proto == "icmp":
			icmp = true
		case p.Lo == p.Hi:
			by[p.Proto] = append(by[p.Proto], fmt.Sprint(p.Lo))
		default:
			by[p.Proto] = append(by[p.Proto], fmt.Sprintf("%d-%d", p.Lo, p.Hi))
		}
	}
	var out []string
	for _, proto := range []string{"tcp", "udp"} {
		if len(by[proto]) > 0 {
			out = append(out, fmt.Sprintf("%s dport { %s } ", proto, strings.Join(by[proto], ", ")))
		}
	}
	if icmp {
		out = append(out, "meta l4proto icmp ")
	}
	return out, nil
}

func fromDesc(from []string) string {
	if len(from) == 0 {
		return ""
	}
	return "[" + strings.Join(from, ",") + "]"
}

func portsDesc(ports []string) string {
	if len(ports) == 0 {
		return " (tous ports)"
	}
	return " (" + strings.Join(ports, " ") + ")"
}
