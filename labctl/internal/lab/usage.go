package lab

import (
	"regexp"
	"sort"
	"strings"
)

// CmdNode est un nœud de l'arbre des commandes d'un module : des sous-commandes (mots fixes)
// et/ou des arguments (emplacements à compléter : MODULE, VM, NOM...).
type CmdNode struct {
	Children map[string]*CmdNode
	Args     []ArgSpec // arguments attendus si la commande s'arrête à ce nœud
	Leaf     bool      // la commande est complète à ce nœud
}

// ArgSpec est un argument tiré de l'aide, ex. « MODULE », « [VM...] », « [--both] ».
type ArgSpec struct {
	Name     string // MODULE, VM, NOM, --both...
	Optional bool
	Variadic bool
}

func newNode() *CmdNode { return &CmdNode{Children: map[string]*CmdNode{}} }

var (
	reUsage = regexp.MustCompile(`^#\s+\./lab\.sh\s+(\S+)\s+(.*)$`)
	reWord  = regexp.MustCompile(`^[a-z][a-z0-9-]*(\|[a-z][a-z0-9-]*)*$`)
)

// ParseUsage construit l'arbre des commandes d'un module à partir des lignes
// « #   ./lab.sh <module> cmd [sous-cmd] ARG [OPT]   description » de son en-tête.
func ParseUsage(module string, lines []string) *CmdNode {
	root := newNode()
	for _, line := range lines {
		m := reUsage.FindStringSubmatch(line)
		if m == nil || m[1] != module {
			continue
		}
		// La syntaxe s'arrête au premier bloc de deux espaces (la description suit)
		syntax := m[2]
		if i := strings.Index(syntax, "  "); i >= 0 {
			syntax = syntax[:i]
		}
		// « status | start | stop | ssh [cmd] » : plusieurs commandes sur une ligne
		for _, alt := range strings.Split(syntax, " | ") {
			addUsage(root, strings.Fields(alt))
		}
	}
	return root
}

func addUsage(root *CmdNode, tokens []string) {
	nodes := []*CmdNode{root}
	i := 0
	for ; i < len(tokens) && reWord.MatchString(tokens[i]); i++ {
		var next []*CmdNode
		for _, n := range nodes {
			for _, w := range strings.Split(tokens[i], "|") {
				c, ok := n.Children[w]
				if !ok {
					c = newNode()
					n.Children[w] = c
				}
				next = append(next, c)
			}
		}
		nodes = next
	}
	var args []ArgSpec
	for _, t := range tokens[i:] {
		a := ArgSpec{Optional: strings.HasPrefix(t, "[")}
		t = strings.Trim(t, "[]")
		if strings.HasSuffix(t, "...") {
			a.Variadic = true
			t = strings.TrimSuffix(t, "...")
		}
		a.Name = t
		args = append(args, a)
	}
	for _, n := range nodes {
		n.Leaf = true
		if len(args) > len(n.Args) {
			n.Args = args
		}
	}
}

// Completer fournit les valeurs dynamiques pour les arguments.
type Completer interface {
	ModuleNames() []string      // modules ayant un réseau (module.env)
	VMs(module string) []string // VMs d'un module
	Snapshots(module, vm string) []string
}

// Complete renvoie les propositions pour le mot en cours, sachant les mots déjà saisis après le module.
func (n *CmdNode) Complete(module string, typed []string, toComplete string, c Completer) []string {
	node := n
	i := 0
	for ; i < len(typed); i++ {
		child, ok := node.Children[typed[i]]
		if !ok {
			break
		}
		node = child
	}
	var out []string
	if i == len(typed) {
		for w := range node.Children {
			out = append(out, w)
		}
	}
	if node.Leaf {
		out = append(out, completeArgs(module, node.Args, typed[i:], c)...)
	}
	return filterPrefix(out, toComplete)
}

// completeArgs propose les valeurs de l'argument suivant, d'après son nom dans l'aide.
func completeArgs(module string, specs []ArgSpec, given []string, c Completer) []string {
	if len(specs) == 0 {
		return nil
	}
	// Argument courant : le suivant, ou le dernier s'il est variadique
	k := len(given)
	var spec ArgSpec
	switch {
	case k < len(specs):
		spec = specs[k]
	case specs[len(specs)-1].Variadic:
		spec = specs[len(specs)-1]
	default:
		return nil
	}
	// Le module visé est le premier argument de type module déjà saisi, sinon le module courant
	target := module
	for j, g := range given {
		if j < len(specs) && isModuleArg(specs[j].Name) {
			target = g
			break
		}
	}
	var out []string
	switch {
	case strings.HasPrefix(spec.Name, "--"):
		out = []string{spec.Name}
	case isModuleArg(spec.Name):
		for _, m := range c.ModuleNames() {
			if !contains(given, m) {
				out = append(out, m)
			}
		}
	case spec.Name == "VM":
		for _, vm := range c.VMs(target) {
			if !contains(given, vm) {
				out = append(out, vm)
			}
		}
	case spec.Name == "NOM" && k > 0 && k-1 < len(specs) && specs[k-1].Name == "VM":
		out = c.Snapshots(target, given[k-1])
	}
	// Un argument optionnel suivi d'une option (--both) : proposer aussi l'option
	if k+1 < len(specs) && spec.Optional && strings.HasPrefix(specs[k+1].Name, "--") {
		out = append(out, specs[k+1].Name)
	}
	return out
}

func isModuleArg(name string) bool {
	switch name {
	case "MODULE", "A", "B", "AUTRE":
		return true
	}
	return false
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func filterPrefix(words []string, prefix string) []string {
	seen := map[string]bool{}
	var out []string
	for _, w := range words {
		if strings.HasPrefix(w, prefix) && !seen[w] {
			seen[w] = true
			out = append(out, w)
		}
	}
	sort.Strings(out)
	return out
}
