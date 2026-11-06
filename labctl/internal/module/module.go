// Package module lit, valide et modifie les modules déclaratifs (modules/<nom>/module.yaml).
// Voir docs/module-yaml.md. Aucun module n'a de code spécifique : tout est décrit dans le YAML.
package module

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
)

// FileName est le nom du fichier de description d'un module.
const FileName = "module.yaml"

// Module est le contenu d'un module.yaml.
type Module struct {
	Module      string            `yaml:"module"`
	Description string            `yaml:"description,omitempty"`
	Network     *Network          `yaml:"network,omitempty"`
	PKI         *PKI              `yaml:"pki,omitempty"`
	DNS         DNS               `yaml:"dns,omitempty"`
	Vars        map[string]string `yaml:"vars,omitempty"`
	Connections []Connection      `yaml:"connections,omitempty"`
	Attach      []Attach          `yaml:"attach,omitempty"`
	Defaults    VMSettings        `yaml:"defaults,omitempty"`
	VMs         map[string]*VM    `yaml:"vms"`

	// Renseignés au chargement
	Dir     string     `yaml:"-"`
	Path    string     `yaml:"-"`
	VMOrder []string   `yaml:"-"` // ordre des VMs dans le fichier
	root    *yaml.Node `yaml:"-"`
	orig    []byte     `yaml:"-"` // contenu lu, pour préserver la mise en forme à l'enregistrement
}

// Network est le réseau isolé du module.
type Network struct {
	Name   string `yaml:"name,omitempty"`
	Subnet string `yaml:"subnet,omitempty"` // 10.66.66.0/24
	Domain string `yaml:"domain,omitempty"`
}

// PKI : CA dérivée du module.
type PKI struct {
	CA string `yaml:"ca"`
}

// DNS : alias, réservations en plus, liens vers d'autres modules (ex-core.conf).
type DNS struct {
	Aliases map[string]string `yaml:"aliases,omitempty"`
	Hosts   []Host            `yaml:"hosts,omitempty"`
	Links   []string          `yaml:"links,omitempty"`
}

// Attach : VM d'un autre module branchée sur le réseau de celui-ci (ex. l'attaquante du module attacker sur le
// réseau des cibles). Elle y reçoit une carte de plus, avec IP réservée, sans route par défaut ni DNS (sa carte
// principale reste celle de son module).
type Attach struct {
	VM    string `yaml:"vm" json:"vm"`                       // <module>/<vm>
	IP    int    `yaml:"ip" json:"ip"`                       // dernier octet sur le réseau de ce module
	MAC   string `yaml:"mac,omitempty" json:"mac,omitempty"` // générée au premier up
	Local bool   `yaml:"-" json:"-"`                         // injectée depuis attach.local.yaml (câblage local), pas du module.yaml
}

// Split renvoie le module et la VM d'un attach.
func (a Attach) Split() (mod, vm string) {
	mod, vm, _ = strings.Cut(a.VM, "/")
	return mod, vm
}

// Host est une réservation DHCP/DNS pour une machine hors des VMs du module.
type Host struct {
	Name string `yaml:"name"`
	IP   int    `yaml:"ip"`
	MAC  string `yaml:"mac"`
}

// VMSettings regroupe les réglages qu'une VM hérite de defaults: s'ils ne sont pas donnés.
type VMSettings struct {
	Image    string   `yaml:"image,omitempty"`
	CPUs     int      `yaml:"cpus,omitempty"`
	Memory   int      `yaml:"memory,omitempty"` // Mio
	Disk     int      `yaml:"disk,omitempty"`   // Gio
	Hardware Hardware `yaml:"hardware,omitempty"`
	Guest    Guest    `yaml:"guest,omitempty"`
	Internet string   `yaml:"internet,omitempty"` // off | install | never
	Snapshot string   `yaml:"snapshot,omitempty"`
}

// VM est une VM du module.
type VM struct {
	VMSettings   `yaml:",inline"`
	Role         string   `yaml:"role,omitempty"`
	IP           int      `yaml:"ip,omitempty"` // dernier octet ; 0 = DHCP libre
	MAC          string   `yaml:"mac,omitempty"`
	Source       *Source  `yaml:"source,omitempty"`
	Windows      *Windows `yaml:"windows,omitempty"`
	Packages     []string `yaml:"packages,omitempty"`
	Files        []File   `yaml:"files,omitempty"`
	Run          []string `yaml:"run,omitempty"`
	Certificates []Cert   `yaml:"certificates,omitempty"`
}

// Hardware : matériel virtuel (vide = défaut q35/virtio/spice).
type Hardware struct {
	Machine    string `yaml:"machine,omitempty"`   // q35 | pc
	DiskBus    string `yaml:"disk_bus,omitempty"`  // virtio | sata | ide
	NICModel   string `yaml:"nic_model,omitempty"` // virtio | e1000
	Graphics   string `yaml:"graphics,omitempty"`  // spice | vnc | none
	Video      string `yaml:"video,omitempty"`     // virtio | vga | qxl
	OSInfo     string `yaml:"osinfo,omitempty"`
	Firmware   string `yaml:"firmware,omitempty"`    // bios (SeaBIOS) | efi (OVMF)
	SecureBoot bool   `yaml:"secure_boot,omitempty"` // efi : Secure Boot, clés Microsoft enrôlées
	TPM        bool   `yaml:"tpm,omitempty"`         // TPM 2.0 émulé (swtpm), ex. Windows 11
}

// Guest : intégration avec la VM.
type Guest struct {
	Agent *bool `yaml:"agent,omitempty"`
}

// Source : disque fourni par un éditeur (pas une image cloud du catalogue).
type Source struct {
	URL      string `yaml:"url,omitempty"`
	Checksum string `yaml:"checksum,omitempty"` // algo:empreinte
	Sums     string `yaml:"sums,omitempty"`     // fichier d'empreintes publié
	File     string `yaml:"file,omitempty"`     // fichier cherché dans Sums (peut contenir *)
	Extract  string `yaml:"extract,omitempty"`  // motif du fichier à prendre dans l'archive
}

// File : fichier du module copié dans la VM.
type File struct {
	Src      string `yaml:"src"`
	Dest     string `yaml:"dest"`
	Mode     string `yaml:"mode,omitempty"`
	Template bool   `yaml:"template,omitempty"`
}

// Cert : certificat émis par la CA du module et installé dans la VM.
type Cert struct {
	Name   string   `yaml:"name"`
	DNS    []string `yaml:"dns,omitempty"`
	IP     bool     `yaml:"ip,omitempty"`
	Cert   string   `yaml:"cert"`
	Key    string   `yaml:"key"`
	Reload string   `yaml:"reload,omitempty"`
}

// Issue est un problème trouvé par la lecture ou la validation.
type Issue struct {
	Error bool   // erreur (sinon avertissement)
	Path  string // ex. vms.web.ip
	Line  int    // 0 si inconnue
	Msg   string
}

func (i Issue) String() string {
	lvl := "avert."
	if i.Error {
		lvl = "erreur"
	}
	loc := i.Path
	if i.Line > 0 {
		loc = fmt.Sprintf("%s (ligne %d)", i.Path, i.Line)
	}
	if loc == "" {
		return fmt.Sprintf("  %-7s %s", lvl, i.Msg)
	}
	return fmt.Sprintf("  %-7s %s : %s", lvl, loc, i.Msg)
}

// ErrNoModule : pas de module.yaml dans ce dossier.
var ErrNoModule = errors.New("pas de " + FileName)

// Load lit modules/<nom>/module.yaml. Une clé inconnue ou mal typée est une erreur (avec sa ligne).
func Load(dir string) (*Module, error) {
	path := filepath.Join(dir, FileName)
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, ErrNoModule
	}
	if err != nil {
		return nil, err
	}
	return Parse(b, dir)
}

// Parse lit le contenu d'un module.yaml (dir : dossier du module, pour les fichiers référencés).
func Parse(b []byte, dir string) (*Module, error) {
	m := &Module{Dir: dir, Path: filepath.Join(dir, FileName)}
	var root yaml.Node
	if err := yaml.Unmarshal(b, &root); err != nil {
		return nil, yamlErr(err)
	}
	if len(root.Content) == 0 {
		return nil, errors.New("fichier vide")
	}
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	if err := dec.Decode(m); err != nil {
		return nil, yamlErr(err)
	}
	m.root, m.orig = &root, b
	if vms := mapValue(root.Content[0], "vms"); vms != nil {
		for i := 0; i+1 < len(vms.Content); i += 2 {
			m.VMOrder = append(m.VMOrder, vms.Content[i].Value)
		}
	}
	for name, vm := range m.VMs {
		if vm == nil { // « srv: » sans valeur = tout par défaut
			m.VMs[name] = &VM{}
		}
	}
	return m, nil
}

// yamlErr traduit les erreurs de la bibliothèque YAML.
func yamlErr(err error) error {
	msg := err.Error()
	msg = strings.TrimPrefix(msg, "yaml: ")
	msg = strings.ReplaceAll(msg, "unmarshal errors:\n  ", "")
	msg = strings.ReplaceAll(msg, "\n  ", " ; ")
	msg = strings.ReplaceAll(msg, "not found in type", "inconnue (faute de frappe ?) dans")
	msg = strings.ReplaceAll(msg, "cannot unmarshal", "type incorrect :")
	msg = strings.ReplaceAll(msg, "line ", "ligne ")
	msg = strings.ReplaceAll(msg, "field ", "clé ")
	msg = strings.ReplaceAll(msg, "module.", "")
	return errors.New(msg)
}

// mapValue renvoie la valeur associée à key dans un nœud mapping (nil si absente).
func mapValue(n *yaml.Node, key string) *yaml.Node {
	if n == nil || n.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			return n.Content[i+1]
		}
	}
	return nil
}

// Line renvoie la ligne d'un chemin « a.b.c » (ou « a.b.0 » pour un élément de liste) ; 0 si introuvable.
// Si le chemin complet n'existe pas, la ligne du parent le plus proche est renvoyée.
func (m *Module) Line(path string) int {
	if m.root == nil || len(m.root.Content) == 0 {
		return 0
	}
	n, line := m.root.Content[0], 0
	for _, part := range strings.Split(path, ".") {
		if part == "" {
			continue
		}
		var next *yaml.Node
		switch n.Kind {
		case yaml.MappingNode:
			for i := 0; i+1 < len(n.Content); i += 2 {
				if n.Content[i].Value == part {
					line, next = n.Content[i].Line, n.Content[i+1]
				}
			}
		case yaml.SequenceNode:
			if idx, err := strconv.Atoi(part); err == nil && idx < len(n.Content) {
				next = n.Content[idx]
				line = next.Line
			}
		}
		if next == nil {
			return line
		}
		n = next
	}
	return line
}
