package lab

import (
	"fmt"
	"io"
	"strings"

	"labs/labctl/internal/virt"
)

// CheckAttach valide une demande de branchement (câblage local) : ref au format <module>/<vm>, module cible et
// VM source connus, VM à IP fixe d'un autre module, adresse libre. ip == 0 : une adresse libre est renvoyée.
// Mêmes règles pour la GUI et la ligne de commande.
func (l *Lab) CheckAttach(on, ref string, ip int) (int, error) {
	mod, vm, ok := strings.Cut(ref, "/")
	if !ok || mod == "" || vm == "" {
		return 0, fmt.Errorf("VM attendue sous la forme <module>/<vm> (ex. attacker/kali)")
	}
	if mod == on {
		return 0, fmt.Errorf("%s est déjà une VM de %s", vm, on)
	}
	mods, err := l.Modules()
	if err != nil {
		return 0, err
	}
	var host, target *Module
	for _, m := range mods {
		switch m.Name {
		case on:
			host = m
		case mod:
			target = m
		}
	}
	if host == nil || host.Env == nil {
		return 0, fmt.Errorf("module cible inconnu ou sans réseau : %s", on)
	}
	if target == nil || target.Env == nil {
		return 0, fmt.Errorf("module inconnu ou sans réseau : %s", mod)
	}
	if _, fixed := target.Env.Hosts[vm]; !fixed {
		return 0, fmt.Errorf("%s n'est pas une VM à IP fixe de %s", vm, mod)
	}
	used := map[int]bool{}
	for _, o := range host.Env.Hosts {
		used[o] = true
	}
	if host.Conf != nil {
		for _, o := range host.Conf.Hosts {
			used[o] = true
		}
	}
	if ip == 0 { // adresse libre proposée (plage des IP fixes : 2-99)
		for o := 50; o <= 99; o++ {
			if !used[o] {
				return o, nil
			}
		}
		for o := 2; o < 50; o++ {
			if !used[o] {
				return o, nil
			}
		}
		return 0, fmt.Errorf("aucune adresse libre (2-99) sur %s", on)
	}
	if ip < 2 || ip > 99 {
		return 0, fmt.Errorf("adresse hors plage (2 à 99)")
	}
	if used[ip] {
		return 0, fmt.Errorf(".%d est déjà pris sur %s", ip, on)
	}
	return ip, nil
}

// AttachVM branche <module>/<vm> sur le réseau du module cible : valide, écrit le câblage local, s'assure du
// réseau, pose la carte à chaud (si la VM tourne) et met à jour le DHCP. ip == 0 : adresse libre choisie.
// Appelée par la GUI (bouton Poser la carte) et la ligne de commande (labctl <module> attach).
func (l *Lab) AttachVM(on, vm string, ip int, out io.Writer) error {
	ip, err := l.CheckAttach(on, vm, ip)
	if err != nil {
		return err
	}
	if err := l.AddLocalAttach(on, vm, ip); err != nil {
		return err
	}
	r, err := l.loadRun(on, out)
	if err != nil {
		return err
	}
	if err := r.ensureNetwork(); err != nil {
		return err
	}
	if err := r.ensureAttach(); err != nil {
		return err
	}
	fmt.Fprintf(out, "ok  %s branchée sur %s (.%d) — câblage local\n", vm, on, ip)
	return r.applyCore()
}

// DetachVM débranche <module>/<vm> du module cible : retire la carte (si posée), supprime le câblage local et
// met à jour le DHCP. Appelée par la GUI (bouton Débrancher) et la ligne de commande (labctl <module> detach).
func (l *Lab) DetachVM(on, vm string, out io.Writer) error {
	wired := false
	for _, a := range l.localAttachFor(on) {
		if strings.EqualFold(a.VM, vm) {
			wired = true
		}
	}
	if !wired {
		return fmt.Errorf("%s n'est pas branchée sur %s (câblage local)", vm, on)
	}
	mac, err := l.DelLocalAttach(on, vm)
	if err != nil {
		return err
	}
	owner, ovm, _ := strings.Cut(vm, "/")
	lv := owner + "-" + ovm
	if mac != "" && DomState(lv) != "absente" {
		v := virt.Shared()
		if ifs, err := v.Interfaces(lv); err == nil {
			for _, it := range ifs {
				if strings.EqualFold(it.MAC, mac) {
					if err := v.DetachInterface(lv, mac, DomState(lv) == "running"); err != nil {
						return fmt.Errorf("carte de %s : %w", lv, err)
					}
				}
			}
		}
	}
	r, err := l.loadRun(on, out)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "ok  %s débranchée de %s — câblage local\n", vm, on)
	return r.applyCore()
}

// VMs branchées d'un module à l'autre (attach: du module.yaml) : le module cible déclare la VM qu'il accueille
// (ex. pjpt : attach: [{vm: attacker/kali, ip: 50}]) ; labctl ajoute à cette VM une carte sur le réseau du
// module cible (up) et la retire (destroy). La carte principale de la VM reste celle de son module.

// attachment : une carte qu'un module cible demande pour une VM d'un autre module.
type attachment struct {
	Module  string // module cible
	Network string // son réseau libvirt
	MAC     string
	IP      int
}

// attachmentsOf renvoie les cartes que les autres modules demandent pour owner/vm.
func (l *Lab) attachmentsOf(owner, vm string) []attachment {
	mods, _ := l.Modules()
	var out []attachment
	for _, m := range mods {
		if m.YAML == nil {
			continue
		}
		for _, a := range m.YAML.Attach {
			if a.VM == owner+"/"+vm {
				out = append(out, attachment{Module: m.Name, Network: m.YAML.NetworkName(), MAC: strings.ToLower(a.MAC), IP: a.IP})
			}
		}
	}
	return out
}

// ensureAttach branche sur le réseau du module les VMs qu'il accueille (MAC générée et écrite au premier
// branchement). Une VM absente est signalée : elle se crée dans son propre module.
func (r *run) ensureAttach() error {
	v := virt.Shared()
	for i, a := range r.y.Attach {
		owner, vm := a.Split()
		lv := owner + "-" + vm
		if DomState(lv) == "absente" {
			r.logf("attention: %s absente : la créer d'abord (labctl %s up %s), puis labctl %s up", lv, owner, vm, r.y.Module)
			continue
		}
		mac := strings.ToLower(a.MAC)
		if mac == "" {
			mac = RandomMAC()
			dest := r.y.Path
			if a.Local { // branchement local : la MAC va dans network.local.yaml, pas dans le module.yaml partagé
				if err := r.l.setLocalAttachMAC(r.y.Module, a.VM, mac); err != nil {
					return err
				}
				dest = r.l.networkLocalPath()
			} else {
				if err := r.y.SetAttachMAC(i, mac); err != nil {
					return err
				}
				if err := r.y.Save(); err != nil {
					return err
				}
			}
			if err := r.reload(); err != nil {
				return err
			}
			r.logf("ok  MAC %s pour %s écrite dans %s", mac, a.VM, dest)
		}
		ifs, err := v.Interfaces(lv)
		if err != nil {
			return err
		}
		model := "virtio"
		for _, it := range ifs {
			if strings.EqualFold(it.MAC, mac) {
				model = ""
				break
			}
			if it.Model != "" {
				model = it.Model // même modèle que ses autres cartes (pilote déjà présent)
			}
		}
		if model == "" {
			continue // déjà branchée
		}
		if err := v.AttachInterface(lv, r.y.NetworkName(), mac, model, DomState(lv) == "running"); err != nil {
			return fmt.Errorf("carte de %s sur %s : %w", lv, r.y.NetworkName(), err)
		}
		r.logf("ok  %s branchée sur %s (.%d, sans route par défaut)", lv, r.y.NetworkName(), a.IP)
	}
	return nil
}

// detachAll retire des VMs accueillies leur carte sur le réseau du module (avant sa suppression).
func (r *run) detachAll() error {
	v := virt.Shared()
	for _, a := range r.y.Attach {
		owner, vm := a.Split()
		lv := owner + "-" + vm
		if DomState(lv) == "absente" || a.MAC == "" {
			continue
		}
		ifs, err := v.Interfaces(lv)
		if err != nil {
			return err
		}
		for _, it := range ifs {
			if strings.EqualFold(it.MAC, a.MAC) {
				if err := v.DetachInterface(lv, a.MAC, DomState(lv) == "running"); err != nil {
					return fmt.Errorf("carte de %s sur %s : %w", lv, r.y.NetworkName(), err)
				}
				r.logf("ok  %s débranchée de %s", lv, r.y.NetworkName())
			}
		}
	}
	return nil
}
