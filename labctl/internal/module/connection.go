package module

import (
	"fmt"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Connexions : ce qu'une partie du module peut joindre (un autre module, une de ses VMs, ou Internet), et
// sur quels ports. Le pare-feu de la passerelle les applique ; dns.links reste le raccourci « tout le
// module vers tout le module, tous ports ».

// Internet : cible d'une connexion vers Internet (ouverte ou fermée à la demande, voir core internet).
const Internet = "internet"

// Connection est une entrée de connections:.
type Connection struct {
	To    string   `yaml:"to" json:"to"`                           // module, module/vm, ou internet
	From  []string `yaml:"from,omitempty" json:"from,omitempty"`   // VMs ou réservations de ce module (défaut : tout le module)
	Ports []string `yaml:"ports,omitempty" json:"ports,omitempty"` // tcp/80, udp/53, tcp/8000-8100, icmp (défaut : tout)
	Via   string   `yaml:"via,omitempty" json:"via,omitempty"`     // direct (défaut) ; portal et proxy à venir
}

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
		case "portal", "proxy":
			c.err(p+".via", "via: %s pas encore disponible (seul direct l'est)", cn.Via)
		default:
			c.err(p+".via", "via inconnu %q (direct)", cn.Via)
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
