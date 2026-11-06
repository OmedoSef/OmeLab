package lab

import (
	"os"
	"path/filepath"
	"strings"

	"labs/labctl/internal/module"

	"go.yaml.in/yaml/v3"
)

// Câblage LOCAL : quelles VMs d'autres modules sont branchées sur quels modules. Ce fichier n'est PAS distribué
// (git-ignoré) : un module cible reste autonome, le branchement est une décision propre à ta machine. Il est
// fusionné dans m.YAML.Attach au chargement (voir loadYAML), si bien que tout le reste du code le traite comme
// un attach: du module.yaml ; seules l'écriture de la MAC et les actions brancher/débrancher visent ce fichier.

// localAttach : une entrée du câblage local (<module cible> <- <module>/<vm>).
type localAttach struct {
	On  string `yaml:"on"`            // module cible (sur le réseau duquel la VM est branchée)
	VM  string `yaml:"vm"`            // <module>/<vm> branchée
	IP  int    `yaml:"ip"`            // dernier octet réservé sur le réseau du module cible
	MAC string `yaml:"mac,omitempty"` // générée au premier branchement
}

type localAttachFile struct {
	Attach []localAttach `yaml:"attach"`
}

func (l *Lab) attachLocalPath() string { return filepath.Join(l.Root, "attach.local.yaml") }

// loadLocalAttach lit attach.local.yaml (liste vide si absent).
func (l *Lab) loadLocalAttach() ([]localAttach, error) {
	b, err := os.ReadFile(l.attachLocalPath())
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var f localAttachFile
	if err := yaml.Unmarshal(b, &f); err != nil {
		return nil, err
	}
	return f.Attach, nil
}

// injectLocalAttach fusionne les branchements locaux dans y.Attach (marqués Local), APRÈS les attach: du
// module.yaml pour garder les index alignés sur le fichier. À appeler sur tout *module.Module chargé.
func (l *Lab) injectLocalAttach(y *module.Module) {
	if y == nil {
		return
	}
	for _, la := range l.localAttachFor(y.Module) {
		y.Attach = append(y.Attach, module.Attach{VM: la.VM, IP: la.IP, MAC: la.MAC, Local: true})
	}
}

// localAttachFor renvoie les branchements locaux visant le module donné.
func (l *Lab) localAttachFor(module string) []localAttach {
	all, _ := l.loadLocalAttach()
	var out []localAttach
	for _, a := range all {
		if a.On == module {
			out = append(out, a)
		}
	}
	return out
}

const localAttachHeader = `# Câblage LOCAL, non distribué (git-ignoré) : quelles VMs sont branchées sur quels modules.
# Édité par les boutons Brancher/Débrancher de la GUI (labctl gui). Rien ici n'est partagé avec les modules :
# un module cible reste autonome. « on » = module cible ; « vm » = <module>/<vm> branchée.
`

// saveLocalAttach réécrit attach.local.yaml (supprimé s'il ne reste rien).
func (l *Lab) saveLocalAttach(entries []localAttach) error {
	if len(entries) == 0 {
		if err := os.Remove(l.attachLocalPath()); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	b, err := yaml.Marshal(localAttachFile{Attach: entries})
	if err != nil {
		return err
	}
	return os.WriteFile(l.attachLocalPath(), append([]byte(localAttachHeader), b...), 0o644)
}

// AddLocalAttach branche <module>/<vm> sur le module cible (remplace une entrée existante de même cible+vm).
func (l *Lab) AddLocalAttach(on, vm string, ip int) error {
	all, err := l.loadLocalAttach()
	if err != nil {
		return err
	}
	out := all[:0:0]
	for _, a := range all {
		if !(a.On == on && strings.EqualFold(a.VM, vm)) {
			out = append(out, a)
		}
	}
	out = append(out, localAttach{On: on, VM: vm, IP: ip})
	return l.saveLocalAttach(out)
}

// DelLocalAttach débranche <module>/<vm> du module cible. Renvoie la MAC retirée (pour détacher la carte).
func (l *Lab) DelLocalAttach(on, vm string) (string, error) {
	all, err := l.loadLocalAttach()
	if err != nil {
		return "", err
	}
	var mac string
	out := all[:0:0]
	for _, a := range all {
		if a.On == on && strings.EqualFold(a.VM, vm) {
			mac = a.MAC
			continue
		}
		out = append(out, a)
	}
	return mac, l.saveLocalAttach(out)
}

// setLocalAttachMAC fixe la MAC d'un branchement local (écrite au premier branchement).
func (l *Lab) setLocalAttachMAC(on, vm, mac string) error {
	all, err := l.loadLocalAttach()
	if err != nil {
		return err
	}
	for i := range all {
		if all[i].On == on && strings.EqualFold(all[i].VM, vm) {
			all[i].MAC = strings.ToLower(mac)
		}
	}
	return l.saveLocalAttach(all)
}
