package module

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

// Connexions : ce qu'une partie du module peut joindre (un autre module, une de ses VMs, ou Internet), et
// sur quels ports. Le pare-feu de la passerelle les applique ; dns.links reste le raccourci « tout le
// module vers tout le module, tous ports ».

// Internet : cible d'une connexion vers Internet (ouverte ou fermée à la demande, voir core internet).
const Internet = "internet"

// Connection est une entrée de connections:.
type Connection struct {
	To     string   `yaml:"to" json:"to"`                             // module, module/vm, ou internet
	From   []string `yaml:"from,omitempty" json:"from,omitempty"`     // VMs ou réservations de ce module (défaut : tout le module)
	Ports  []string `yaml:"ports,omitempty" json:"ports,omitempty"`   // tcp/80, udp/53, tcp/8000-8100, icmp (défaut : tout)
	Via    string   `yaml:"via,omitempty" json:"via,omitempty"`       // direct (défaut), portal, proxy
	Portal *Portal  `yaml:"portal,omitempty" json:"portal,omitempty"` // options de via: portal
	Proxy  *Proxy   `yaml:"proxy,omitempty" json:"proxy,omitempty"`   // options de via: proxy
	Local  bool     `yaml:"-" json:"local,omitempty"`                 // injectée depuis network.local.yaml (propre au poste), pas du module.yaml
}

// Proxy : options d'une connexion qui passe par le proxy transparent de la passerelle (via: proxy, mitmproxy).
// Le HTTP et le HTTPS (tcp/80, tcp/443 et Ports en plus) sont redirigés vers le proxy ; le reste suit ports:.
type Proxy struct {
	Mode  string   `yaml:"mode,omitempty" json:"mode,omitempty"`   // log (défaut) | filter | intercept
	Allow []string `yaml:"allow,omitempty" json:"allow,omitempty"` // filter : domaines autorisés (*.debian.org)
	Deny  []string `yaml:"deny,omitempty" json:"deny,omitempty"`   // filter : domaines refusés (le reste passe)
	Ports []string `yaml:"ports,omitempty" json:"ports,omitempty"` // ports HTTP(S) en plus de tcp/80 et tcp/443
}

// ProxyModes : modes du proxy.
var ProxyModes = []string{"log", "filter", "intercept"}

var reDomainPattern = regexp.MustCompile(`^(\*\.)?([a-z0-9]([a-z0-9-]*[a-z0-9])?\.)*[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)

// ProxyPorts renvoie les ports TCP redirigés vers le proxy (80, 443 et les ports en plus).
func (p *Proxy) ProxyPorts() []Port {
	out := []Port{{Proto: "tcp", Lo: 80, Hi: 80}, {Proto: "tcp", Lo: 443, Hi: 443}}
	if p != nil {
		for _, s := range p.Ports {
			if pt, err := ParsePort(s); err == nil && pt.Proto == "tcp" {
				out = append(out, pt)
			}
		}
	}
	return out
}

// Portal : options d'une connexion derrière un portail captif (via: portal). Avant authentification, le HTTP
// vers la cible est redirigé vers le portail de la passerelle et le reste refusé ; après, la connexion est
// ouverte pendant Duration.
type Portal struct {
	Auth     string `yaml:"auth,omitempty" json:"auth,omitempty"`         // click (défaut) | password | terms
	Password string `yaml:"password,omitempty" json:"password,omitempty"` // auth: password (mot de passe de lab, en clair)
	Duration string `yaml:"duration,omitempty" json:"duration,omitempty"` // durée d'une session (défaut 1h ; 1m à 24h)
	Title    string `yaml:"title,omitempty" json:"title,omitempty"`       // titre de la page
	Detect   *bool  `yaml:"detect,omitempty" json:"detect,omitempty"`     // rediriger les tests de connectivité des OS (défaut oui)
	API      bool   `yaml:"api,omitempty" json:"api,omitempty"`           // annonce RFC 8910 (DHCP 114) + API RFC 8908
	HTTPS    string `yaml:"https,omitempty" json:"https,omitempty"`       // HTTPS non authentifié : reject (défaut) | drop
}

// PortalAuths : modes d'authentification d'un portail.
var PortalAuths = []string{"click", "password", "terms"}

// Session renvoie la durée d'une session (défaut 1 h).
func (p *Portal) Session() time.Duration {
	if p == nil || p.Duration == "" {
		return time.Hour
	}
	d, _ := time.ParseDuration(p.Duration)
	return d
}

// Detects : les tests de connectivité des OS sont-ils redirigés (défaut oui) ?
func (p *Portal) Detects() bool { return p == nil || p.Detect == nil || *p.Detect }

// Port est un élément de ports: analysé.
type Port struct {
	Proto  string // tcp, udp, icmp
	Lo, Hi int    // 0 pour icmp
}

func (p Port) String() string {
	switch {
	case p.Proto == "icmp":
		return "icmp"
	case p.Lo == p.Hi:
		return fmt.Sprintf("%s/%d", p.Proto, p.Lo)
	default:
		return fmt.Sprintf("%s/%d-%d", p.Proto, p.Lo, p.Hi)
	}
}

// ParsePort lit tcp/80, udp/53, tcp/8000-8100 ou icmp.
func ParsePort(s string) (Port, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "icmp" {
		return Port{Proto: "icmp"}, nil
	}
	proto, rng, ok := strings.Cut(s, "/")
	if !ok || (proto != "tcp" && proto != "udp") {
		return Port{}, fmt.Errorf("port invalide %q (tcp/80, udp/53, tcp/8000-8100, icmp)", s)
	}
	lo, hi, isRange := strings.Cut(rng, "-")
	if !isRange {
		hi = lo
	}
	a, err1 := strconv.Atoi(lo)
	b, err2 := strconv.Atoi(hi)
	if err1 != nil || err2 != nil || a < 1 || b > 65535 || a > b {
		return Port{}, fmt.Errorf("port invalide %q (1 à 65535, plage croissante)", s)
	}
	return Port{Proto: proto, Lo: a, Hi: b}, nil
}

// Target sépare la cible : module (vide pour Internet) et VM éventuelle.
func (c Connection) Target() (mod, vm string) {
	if c.To == Internet {
		return "", ""
	}
	mod, vm, _ = strings.Cut(c.To, "/")
	return mod, vm
}

// checkConnections valide connections: (Context.Targets : noms à IP fixe de chaque module).
func (c *checker) checkConnections(ctx Context) {
	m := c.m
	links := map[string]bool{}
	for _, l := range m.DNS.Links {
		links[l] = true
	}
	for i, cn := range m.Connections {
		p := fmt.Sprintf("connections.%d", i)
		switch cn.Via {
		case "", "direct":
		case "portal":
			c.checkPortal(p, cn, ctx)
		case "proxy":
			c.checkProxy(p, cn)
		default:
			c.err(p+".via", "via inconnu %q (direct, portal, proxy)", cn.Via)
		}
		if cn.Portal != nil && cn.Via != "portal" {
			c.err(p+".portal", "portal: sans via: portal")
		}
		if cn.Proxy != nil && cn.Via != "proxy" {
			c.err(p+".proxy", "proxy: sans via: proxy")
		}
		if mod, vm := cn.Target(); cn.To == "" {
			c.err(p+".to", "to obligatoire : un module, module/vm, ou internet")
		} else if cn.To != Internet {
			switch {
			case mod == m.Module:
				c.err(p+".to", "un module est toujours relié à lui-même")
			case !contains(ctx.Modules, mod):
				c.err(p+".to", "module inconnu %q", mod)
			case links[mod]:
				c.err(p+".to", "%s est déjà dans dns.links (tout autorisé) : retirer l'un des deux", mod)
			case vm != "" && ctx.Targets != nil && !contains(ctx.Targets[mod], vm):
				c.err(p+".to", "%s n'est pas une VM à IP fixe (ni une réservation) du module %s", vm, mod)
			}
		}
		for j, f := range cn.From {
			fp := fmt.Sprintf("%s.from.%d", p, j)
			if vm := m.VMs[f]; vm != nil {
				if vm.IP == 0 {
					c.err(fp, "%s n'a pas d'IP fixe (ip:) : adresse inconnue du pare-feu", f)
				}
			} else if !m.hasHost(f) {
				c.err(fp, "%s : ni une VM ni une réservation du module", f)
			}
		}
		for j, s := range cn.Ports {
			if _, err := ParsePort(s); err != nil {
				c.err(fmt.Sprintf("%s.ports.%d", p, j), "%v", err)
			}
		}
	}
}

func (c *checker) checkPortal(p string, cn Connection, ctx Context) {
	pt := cn.Portal
	if pt == nil {
		pt = &Portal{}
	}
	pp := p + ".portal"
	switch pt.Auth {
	case "", "click", "terms":
		if pt.Password != "" {
			c.err(pp+".password", "password sans auth: password")
		}
	case "password":
		if pt.Password == "" {
			c.err(pp+".password", "auth: password demande un password")
		}
	default:
		c.err(pp+".auth", "auth inconnu %q (%s)", pt.Auth, strings.Join(PortalAuths, ", "))
	}
	if pt.Duration != "" {
		if d, err := time.ParseDuration(pt.Duration); err != nil || d < time.Minute || d > 24*time.Hour {
			c.err(pp+".duration", "durée invalide %q (de 1m à 24h, ex. 30m, 2h)", pt.Duration)
		}
	}
	if pt.HTTPS != "" && pt.HTTPS != "reject" && pt.HTTPS != "drop" {
		c.err(pp+".https", "https inconnu %q (reject, drop)", pt.HTTPS)
	}
	// Une VM qui ne doit jamais sortir ne passe pas non plus par un portail vers Internet
	if cn.To == Internet {
		for _, f := range cn.From {
			if vm := c.m.VMs[f]; vm != nil && c.m.Effective(f, ctx.Settings).Internet == "never" {
				c.err(p+".from", "%s a internet: never : pas de portail vers Internet", f)
			}
		}
	}
}

func (c *checker) checkProxy(p string, cn Connection) {
	px := cn.Proxy
	if px == nil {
		px = &Proxy{}
	}
	pp := p + ".proxy"
	switch px.Mode {
	case "", "log", "intercept":
		if len(px.Allow)+len(px.Deny) > 0 {
			c.err(pp, "allow et deny demandent mode: filter")
		}
	case "filter":
		if len(px.Allow) > 0 && len(px.Deny) > 0 {
			c.err(pp, "allow ou deny, pas les deux (allow : seuls ces domaines ; deny : tout sauf ces domaines)")
		}
		if len(px.Allow)+len(px.Deny) == 0 {
			c.err(pp, "mode: filter demande allow (domaines autorisés) ou deny (domaines refusés)")
		}
	default:
		c.err(pp+".mode", "mode inconnu %q (%s)", px.Mode, strings.Join(ProxyModes, ", "))
	}
	for _, kv := range []struct {
		k string
		v []string
	}{{"allow", px.Allow}, {"deny", px.Deny}} {
		for j, d := range kv.v {
			if !reDomainPattern.MatchString(strings.ToLower(d)) {
				c.err(fmt.Sprintf("%s.%s.%d", pp, kv.k, j), "domaine invalide %q (ex. github.com, *.debian.org)", d)
			}
		}
	}
	for j, s := range px.Ports {
		if pt, err := ParsePort(s); err != nil || pt.Proto != "tcp" {
			c.err(fmt.Sprintf("%s.ports.%d", pp, j), "port TCP attendu (ex. tcp/8080), pas %q", s)
		}
	}
}

// AddConnection ajoute une entrée à connections: (une ligne {to, from, ports}).
func (m *Module) AddConnection(cn Connection) {
	seq := mapValue(m.doc(), "connections")
	if seq == nil || seq.Kind != yaml.SequenceNode {
		seq = &yaml.Node{Kind: yaml.SequenceNode}
		setKey(m.doc(), "connections", seq)
	}
	seq.Style = 0
	list := func(v []string) *yaml.Node {
		n := &yaml.Node{Kind: yaml.SequenceNode, Style: yaml.FlowStyle}
		for _, s := range v {
			n.Content = append(n.Content, scalar(s))
		}
		return n
	}
	item := &yaml.Node{Kind: yaml.MappingNode, Style: yaml.FlowStyle, Content: []*yaml.Node{scalar("to"), scalar(cn.To)}}
	if len(cn.From) > 0 {
		item.Content = append(item.Content, scalar("from"), list(cn.From))
	}
	if len(cn.Ports) > 0 {
		item.Content = append(item.Content, scalar("ports"), list(cn.Ports))
	}
	if cn.Via != "" && cn.Via != "direct" {
		item.Content = append(item.Content, scalar("via"), scalar(cn.Via))
	}
	if px := cn.Proxy; px != nil && cn.Via == "proxy" {
		po := &yaml.Node{Kind: yaml.MappingNode, Style: yaml.FlowStyle}
		if px.Mode != "" {
			po.Content = append(po.Content, scalar("mode"), scalar(px.Mode))
		}
		for _, kv := range []struct {
			k string
			v []string
		}{{"allow", px.Allow}, {"deny", px.Deny}, {"ports", px.Ports}} {
			if len(kv.v) > 0 {
				po.Content = append(po.Content, scalar(kv.k), list(kv.v))
			}
		}
		if len(po.Content) > 0 {
			item.Content = append(item.Content, scalar("proxy"), po)
		}
	}
	if pt := cn.Portal; pt != nil && cn.Via == "portal" {
		po := &yaml.Node{Kind: yaml.MappingNode, Style: yaml.FlowStyle}
		add := func(k, v string) {
			if v != "" {
				po.Content = append(po.Content, scalar(k), scalar(v))
			}
		}
		add("auth", pt.Auth)
		add("password", pt.Password)
		add("duration", pt.Duration)
		add("title", pt.Title)
		if pt.Detect != nil && !*pt.Detect {
			add("detect", "false")
		}
		if pt.API {
			add("api", "true")
		}
		add("https", pt.HTTPS)
		if len(po.Content) > 0 {
			item.Content = append(item.Content, scalar("portal"), po)
		}
	}
	seq.Content = append(seq.Content, item)
}

// DelConnection retire l'entrée i de connections: (la clé disparaît avec la dernière).
func (m *Module) DelConnection(i int) bool {
	seq := mapValue(m.doc(), "connections")
	if seq == nil || seq.Kind != yaml.SequenceNode || i < 0 || i >= len(seq.Content) {
		return false
	}
	seq.Content = append(seq.Content[:i], seq.Content[i+1:]...)
	if len(seq.Content) == 0 {
		delKey(m.doc(), "connections")
	}
	return true
}
