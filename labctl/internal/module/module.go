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
	Module      string            `yaml:"module"`                // nom du module (= nom de son dossier)
	Description string            `yaml:"description,omitempty"` // affichée par labctl, la GUI et la complétion
	Network     *Network          `yaml:"network,omitempty"`
	PKI         *PKI              `yaml:"pki,omitempty"`
	DNS         DNS               `yaml:"dns,omitempty"`
	Vars        map[string]string `yaml:"vars,omitempty"` // valeurs nommées, reprises partout via ${var.<nom>} ; lab.yaml et lab.local.yaml les surchargent
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
	Name      string `yaml:"name,omitempty"`      // réseau libvirt (défaut : <module>-lab)
	Subnet    string `yaml:"subnet,omitempty"`    // 10.66.66.0/24
	Domain    string `yaml:"domain,omitempty"`    // domaine complet, pris tel quel (ex. pjpt.local)
	Subdomain string `yaml:"subdomain,omitempty"` // libellé collé sous le domaine racine : <subdomain>.<racine>
}

// PKI : CA dérivée du module.
type PKI struct {
	CA string `yaml:"ca"` // nom de la CA dérivée, limitée au domaine et au sous-réseau du module
}

// DNS : alias, réservations en plus, liens vers d'autres modules (ex-core.conf).
type DNS struct {
	Aliases map[string]string `yaml:"aliases,omitempty"` // <alias>.<domaine> -> VM du module (ou nom complet)
	Hosts   []Host            `yaml:"hosts,omitempty"`   // réservations DHCP/DNS de machines hors vms:
	Links   []string          `yaml:"links,omitempty"`   // modules que celui-ci peut joindre et résoudre (tout, tous ports)
}

// Attach : VM d'un autre module branchée sur le réseau de celui-ci (ex. l'attaquante du module attacker sur le
// réseau des cibles). Elle y reçoit une carte de plus, avec IP réservée, sans route par défaut ni DNS (sa carte
// principale reste celle de son module).
type Attach struct {
	VM    string `yaml:"vm" json:"vm"`                       // <module>/<vm>
	IP    int    `yaml:"ip" json:"ip"`                       // dernier octet sur le réseau de ce module
	MAC   string `yaml:"mac,omitempty" json:"mac,omitempty"` // générée au premier up
	Local bool   `yaml:"-" json:"-"`                         // injectée depuis network.local.yaml (câblage local), pas du module.yaml
}

// Split renvoie le module et la VM d'un attach.
func (a Attach) Split() (mod, vm string) {
	mod, vm, _ = strings.Cut(a.VM, "/")
	return mod, vm
}

// Host est une réservation DHCP/DNS pour une machine hors des VMs du module.
type Host struct {
	Name string `yaml:"name"` // nom DNS (<name>.<domaine>)
	IP   int    `yaml:"ip"`   // dernier octet réservé
	MAC  string `yaml:"mac"`  // MAC de la machine (aa:bb:cc:dd:ee:ff)
}

// VMSettings regroupe les réglages qu'une VM hérite de defaults: s'ils ne sont pas donnés.
type VMSettings struct {
	Image    string   `yaml:"image,omitempty"`  // image cloud du catalogue (labctl image list) ; défaut debian13
	CPUs     int      `yaml:"cpus,omitempty"`   // nombre de vCPU
	Memory   int      `yaml:"memory,omitempty"` // Mio
	Disk     int      `yaml:"disk,omitempty"`   // Gio
	Hardware Hardware `yaml:"hardware,omitempty"`
	Guest    Guest    `yaml:"guest,omitempty"`
	Internet string   `yaml:"internet,omitempty"` // off | install | never
	Snapshot string   `yaml:"snapshot,omitempty"` // snapshot pris une fois la VM prête (ex. clean), base de restore
	Keyboard string   `yaml:"keyboard,omitempty"` // disposition clavier ; défaut : celle de l'hôte (LABS_KEYBOARD/localectl)
}

// VM est une VM du module.
type VM struct {
	VMSettings   `yaml:",inline"`
	Role         string   `yaml:"role,omitempty"` // attacker, target, server, client… (affichage, filtres ; une target reste sans Internet)
	IP           int      `yaml:"ip,omitempty"`   // dernier octet ; 0 = DHCP libre
	MAC          string   `yaml:"mac,omitempty"`  // générée et écrite ici au premier up
	Source       *Source  `yaml:"source,omitempty"`
	Windows      *Windows `yaml:"windows,omitempty"`
	Packages     []string `yaml:"packages,omitempty"`     // paquets installés par cloud-init
	Files        []File   `yaml:"files,omitempty"`        // fichiers du module copiés dans la VM
	Run          []string `yaml:"run,omitempty"`          // commandes lancées à la création (shell ; PowerShell sous Windows)
	Certificates []Cert   `yaml:"certificates,omitempty"` // certificats émis par la CA du module et installés dans la VM
}

// Hardware : matériel virtuel (vide = défaut q35/virtio/spice).
type Hardware struct {
	Machine    string `yaml:"machine,omitempty"`     // q35 | pc
	DiskBus    string `yaml:"disk_bus,omitempty"`    // virtio | sata | ide
	NICModel   string `yaml:"nic_model,omitempty"`   // virtio | e1000
	Graphics   string `yaml:"graphics,omitempty"`    // spice | vnc | none
	Video      string `yaml:"video,omitempty"`       // virtio | vga | qxl
	OSInfo     string `yaml:"osinfo,omitempty"`      // identifiant osinfo de libvirt (ex. debiantesting, win2k22)
	Firmware   string `yaml:"firmware,omitempty"`    // bios (SeaBIOS) | efi (OVMF)
	SecureBoot bool   `yaml:"secure_boot,omitempty"` // efi : Secure Boot, clés Microsoft enrôlées
	TPM        bool   `yaml:"tpm,omitempty"`         // TPM 2.0 émulé (swtpm), ex. Windows 11
}

// Guest : intégration avec la VM.
type Guest struct {
	Agent *bool `yaml:"agent,omitempty"` // agent invité QEMU (clavier, IP, commandes) ; défaut : oui pour une image cloud
}

// Source : disque fourni par un éditeur (pas une image cloud du catalogue).
type Source struct {
	URL      string `yaml:"url,omitempty"`      // adresse https du disque ou de l'archive
	Checksum string `yaml:"checksum,omitempty"` // algo:empreinte
	Sums     string `yaml:"sums,omitempty"`     // fichier d'empreintes publié
	File     string `yaml:"file,omitempty"`     // fichier cherché dans Sums (peut contenir *)
	Extract  string `yaml:"extract,omitempty"`  // motif du fichier à prendre dans l'archive
}

// File : fichier du module copié dans la VM.
type File struct {
	Src      string `yaml:"src"`                // relatif au dossier du module (ex. files/x.conf) ; jamais hors du module
	Dest     string `yaml:"dest"`               // chemin absolu dans la VM (Windows : C:/dossier/fichier)
	Mode     string `yaml:"mode,omitempty"`     // droits (ex. 0644)
	Template bool   `yaml:"template,omitempty"` // remplace les ${...} du contenu
}

// Cert : certificat émis par la CA du module et installé dans la VM.
type Cert struct {
	Name   string   `yaml:"name"`             // nom du certificat ; SAN <name>.<domaine du module>
	DNS    []string `yaml:"dns,omitempty"`    // noms DNS en plus (SAN)
	IP     bool     `yaml:"ip,omitempty"`     // ajoute l'IP de la VM aux SAN
	Cert   string   `yaml:"cert"`             // chemin absolu du certificat dans la VM
	Key    string   `yaml:"key"`              // chemin absolu de la clé dans la VM
	Reload string   `yaml:"reload,omitempty"` // commande relancée après installation (ex. systemctl reload nginx)
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
