package lab

import (
	"strings"

	"labs/labctl/internal/module"
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

// loadLocalAttach renvoie le câblage local (section attach de network.local.yaml, ou l'ancien attach.local.yaml).
func (l *Lab) loadLocalAttach() ([]localAttach, error) {
	f, err := l.loadNetworkLocal()
	return f.Attach, err
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

// LocalAttachRefs renvoie les <module>/<vm> branchés localement sur le module donné (pour l'autocomplétion).
func (l *Lab) LocalAttachRefs(on string) []string {
	var out []string
	for _, a := range l.localAttachFor(on) {
		out = append(out, a.VM)
	}
	return out
}

// AttachCandidates renvoie les <module>/<vm> à IP fixe des autres modules, branchables sur `on` et pas déjà
// branchés (pour l'autocomplétion de `labctl <module> attach`).
func (l *Lab) AttachCandidates(on string) []string {
	already := map[string]bool{}
	for _, r := range l.LocalAttachRefs(on) {
		already[strings.ToLower(r)] = true
	}
	mods, _ := l.Modules()
	var out []string
	for _, m := range mods {
		if m.Name == on || m.Env == nil {
			continue
		}
		for vm := range m.Env.Hosts {
			ref := m.Name + "/" + vm
			if !already[strings.ToLower(ref)] {
				out = append(out, ref)
			}
		}
	}
	return out
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

// saveLocalAttach réécrit la section attach de network.local.yaml (les connexions locales sont préservées).
func (l *Lab) saveLocalAttach(entries []localAttach) error {
	f, err := l.loadNetworkLocal()
	if err != nil {
		return err
	}
	f.Attach = entries
	return l.saveNetworkLocal(f)
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
