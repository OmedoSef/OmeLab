package module

import (
	"bytes"
	"fmt"
	"os"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"

	"labs/labctl/internal/modfiles"
)

// Les éditions modifient l'arbre YAML (et non une structure réencodée) : commentaires, ordre des clés et
// mise en forme du fichier sont conservés. Après une édition, appeler Save puis recharger le module.

func (m *Module) doc() *yaml.Node { return m.root.Content[0] }

func scalar(v string) *yaml.Node { return &yaml.Node{Kind: yaml.ScalarNode, Value: v} }

func intNode(v int) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: strconv.Itoa(v)}
}

// child renvoie la valeur de key dans le mapping n, en la créant (mapping vide) si create.
func child(n *yaml.Node, key string, create bool) *yaml.Node {
	if v := mapValue(n, key); v != nil {
		if v.Kind == yaml.ScalarNode && v.Tag == "!!null" && create { // « dns: » vide
			*v = yaml.Node{Kind: yaml.MappingNode}
		}
		return v
	}
	if !create {
		return nil
	}
	v := &yaml.Node{Kind: yaml.MappingNode}
	n.Content = append(n.Content, scalar(key), v)
	return v
}

// setKey remplace (ou ajoute) key: value dans le mapping n.
func setKey(n *yaml.Node, key string, value *yaml.Node) {
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			// garder les commentaires de fin de ligne
			value.LineComment = n.Content[i+1].LineComment
			n.Content[i+1] = value
			return
		}
	}
	n.Content = append(n.Content, scalar(key), value)
}

// delKey retire key du mapping n ; renvoie false si elle n'existait pas.
func delKey(n *yaml.Node, key string) bool {
	if n == nil {
		return false
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			n.Content = append(n.Content[:i], n.Content[i+2:]...)
			return true
		}
	}
	return false
}

// SetAlias ajoute ou remplace dns.aliases.<alias>.
func (m *Module) SetAlias(alias, target string) {
	aliases := child(child(m.doc(), "dns", true), "aliases", true)
	aliases.Style = 0
	setKey(aliases, alias, scalar(target))
}

// DelAlias retire un alias (et la section aliases si elle devient vide).
func (m *Module) DelAlias(alias string) bool {
	dns := child(m.doc(), "dns", false)
	aliases := child(dns, "aliases", false)
	ok := delKey(aliases, alias)
	if aliases != nil && len(aliases.Content) == 0 {
		delKey(dns, "aliases")
	}
	return ok
}

// AddHost ajoute une réservation dns.hosts (une ligne {name, ip, mac}).
func (m *Module) AddHost(h Host) {
	dns := child(m.doc(), "dns", true)
	hosts := mapValue(dns, "hosts")
	if hosts == nil || hosts.Kind != yaml.SequenceNode {
		hosts = &yaml.Node{Kind: yaml.SequenceNode}
		setKey(dns, "hosts", hosts)
	}
	hosts.Style = 0
	hosts.Content = append(hosts.Content, &yaml.Node{Kind: yaml.MappingNode, Style: yaml.FlowStyle, Content: []*yaml.Node{
		scalar("name"), scalar(h.Name), scalar("ip"), intNode(h.IP), scalar("mac"), scalar(h.MAC),
	}})
}

// DelHost retire la réservation name.
func (m *Module) DelHost(name string) bool {
	hosts := mapValue(child(m.doc(), "dns", false), "hosts")
	if hosts == nil {
		return false
	}
	for i, h := range hosts.Content {
		if v := mapValue(h, "name"); v != nil && v.Value == name {
			hosts.Content = append(hosts.Content[:i], hosts.Content[i+1:]...)
			if len(hosts.Content) == 0 {
				delKey(child(m.doc(), "dns", false), "hosts")
			}
			return true
		}
	}
	return false
}

// SetLink ajoute (on) ou retire le lien vers le module other.
func (m *Module) SetLink(other string, on bool) {
	dns := child(m.doc(), "dns", true)
	links := mapValue(dns, "links")
	if links == nil || links.Kind != yaml.SequenceNode {
		if !on {
			return
		}
		links = &yaml.Node{Kind: yaml.SequenceNode, Style: yaml.FlowStyle}
		setKey(dns, "links", links)
	}
	for i, l := range links.Content {
		if l.Value == other {
			if !on {
				links.Content = append(links.Content[:i], links.Content[i+1:]...)
				if len(links.Content) == 0 {
					delKey(dns, "links")
				}
			}
			return
		}
	}
	if on {
		links.Content = append(links.Content, scalar(other))
	}
}

// SetVMValue fixe vms.<vm>.<key> (ex. mac générée à la création).
func (m *Module) SetVMValue(vm, key, value string) error {
	vms := mapValue(m.doc(), "vms")
	n := mapValue(vms, vm)
	if n == nil {
		return fmt.Errorf("VM inconnue : %s", vm)
	}
	if n.Kind != yaml.MappingNode { // « srv: » ou « srv: {} »
		n.Kind, n.Tag, n.Value, n.Style = yaml.MappingNode, "", "", yaml.FlowStyle
	}
	if key == "mac" && mapValue(n, "mac") == nil { // la MAC à côté de l'IP qu'elle réserve
		for i := 0; i+1 < len(n.Content); i += 2 {
			if n.Content[i].Value == "ip" {
				n.Content = append(n.Content[:i+2], append([]*yaml.Node{scalar("mac"), scalar(value)}, n.Content[i+2:]...)...)
				return nil
			}
		}
	}
	setKey(n, key, scalar(value))
	return nil
}

// SetAttachMAC fixe attach.<i>.mac (MAC générée au premier branchement).
func (m *Module) SetAttachMAC(i int, mac string) error {
	seq := mapValue(m.doc(), "attach")
	if seq == nil || seq.Kind != yaml.SequenceNode || i < 0 || i >= len(seq.Content) {
		return fmt.Errorf("pas d'attach n° %d", i)
	}
	setKey(seq.Content[i], "mac", scalar(mac))
	return nil
}

// SetNetwork écrit la section network (sous-réseau choisi au premier up).
func (m *Module) SetNetwork(name, subnet string) {
	if mapValue(m.doc(), "network") == nil { // nouvelle section : avant vms:, là où on la cherche
		doc := m.doc()
		at := len(doc.Content)
		for i := 0; i+1 < len(doc.Content); i += 2 {
			if doc.Content[i].Value == "vms" {
				at = i
			}
		}
		node := &yaml.Node{Kind: yaml.MappingNode}
		doc.Content = append(doc.Content[:at], append([]*yaml.Node{scalar("network"), node}, doc.Content[at:]...)...)
	}
	net := child(m.doc(), "network", true)
	if name != "" && mapValue(net, "name") == nil {
		setKey(net, "name", scalar(name))
	}
	setKey(net, "subnet", scalar(subnet))
}

// Bytes renvoie le fichier tel qu'il serait enregistré : réencodé par la bibliothèque YAML, puis
// remis en forme d'après l'original (voir keepFormat).
func (m *Module) Bytes() ([]byte, error) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(m.root); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	if m.orig == nil {
		return buf.Bytes(), nil
	}
	return keepFormat(m.orig, buf.Bytes()), nil
}

// keepFormat préserve la mise en forme de l'original : la bibliothèque YAML réencode tout le fichier
// (alignement des commentaires perdu, lignes vides supprimées). Les deux versions sont comparées ligne à
// ligne en ignorant les espaces : une ligne inchangée reprend son texte d'origine exact, les lignes vides
// d'origine sont conservées, seules les lignes réellement modifiées sont nouvelles.
func keepFormat(orig, enc []byte) []byte {
	a := strings.Split(strings.TrimRight(string(orig), "\n"), "\n")
	b := strings.Split(strings.TrimRight(string(enc), "\n"), "\n")
	norm := func(s string) string { return strings.Join(strings.Fields(s), " ") }
	na, nb := make([]string, len(a)), make([]string, len(b))
	for i := range a {
		na[i] = norm(a[i])
	}
	for i := range b {
		nb[i] = norm(b[i])
	}
	// plus longue sous-séquence commune (fichiers courts)
	lcs := make([][]int, len(a)+1)
	for i := range lcs {
		lcs[i] = make([]int, len(b)+1)
	}
	for i := len(a) - 1; i >= 0; i-- {
		for j := len(b) - 1; j >= 0; j-- {
			if na[i] == nb[j] && na[i] != "" {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else {
				lcs[i][j] = max(lcs[i+1][j], lcs[i][j+1])
			}
		}
	}
	var out []string
	i, j := 0, 0
	for i < len(a) || j < len(b) {
		switch {
		case i < len(a) && j < len(b) && na[i] == nb[j] && na[i] != "":
			out = append(out, a[i]) // ligne inchangée : texte d'origine
			i, j = i+1, j+1
		case i < len(a) && na[i] == "":
			out = append(out, a[i]) // ligne vide d'origine : conservée
			i++
		case j < len(b) && (i == len(a) || lcs[i][j+1] >= lcs[i+1][j]):
			out = append(out, b[j]) // ligne nouvelle ou modifiée
			j++
		default:
			i++ // ligne supprimée
		}
	}
	// pas de lignes vides en double laissées par une suppression
	var clean []string
	for k, line := range out {
		if line == "" && k > 0 && out[k-1] == "" {
			continue
		}
		clean = append(clean, line)
	}
	return []byte(strings.Join(clean, "\n") + "\n")
}

// Save enregistre le fichier (remplacement atomique) après avoir vérifié qu'il se relit.
func (m *Module) Save() error {
	b, err := m.Bytes()
	if err != nil {
		return err
	}
	if _, err := Parse(b, m.Dir); err != nil {
		return fmt.Errorf("édition refusée, le fichier deviendrait illisible : %w", err)
	}
	// Écriture atomique par un fichier temporaire exclusif : un module tiers qui livrerait un lien (module.yaml.tmp,
	// module.yaml…) vers un fichier de l'hôte ne le ferait pas écrire
	if err := NotSymlink(m.Path); err != nil {
		return err
	}
	return modfiles.WriteAtomic(m.Path, bytes.NewReader(b), maxModuleFile)
}

// maxModuleFile : taille maximale d'un module.yaml écrit par labctl.
const maxModuleFile = 4 << 20

// NotSymlink refuse un module.yaml qui serait un lien symbolique : labctl ne lit ni n'écrit un fichier hors du module
// par ce biais.
func NotSymlink(path string) error {
	if fi, err := os.Lstat(path); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%s est un lien symbolique : refusé (un module.yaml est un fichier ordinaire)", path)
	}
	return nil
}
