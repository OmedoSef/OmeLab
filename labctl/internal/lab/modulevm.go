package lab

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"labs/labctl/internal/module"
	"labs/labctl/internal/virt"
	"labs/labctl/internal/vmdef"
)

// Moteur des modules : création et reprise des VMs (voir moduleup.go).

// ---------------------------------------------------------------- VMs

// Diff est un écart entre le YAML et une VM existante.
type Diff struct {
	Field string `json:"field"`
	Want  string `json:"want"`
	Have  string `json:"have"`
	Fix   bool   `json:"fix"` // corrigé par up --apply-changes
}

func (d Diff) String() string {
	s := fmt.Sprintf("%s : %s dans le YAML, %s en réalité", d.Field, d.Want, d.Have)
	if !d.Fix {
		s += " (non modifiable : recréer la VM)"
	}
	return s
}

// diffs compare une VM existante à sa description ; isolation = erreur bloquante.
func (r *run) diffs(vm string) ([]Diff, error) {
	v, lv, e := r.l.Hypervisor(), r.libvirtName(vm), r.eff(vm)
	ifs, err := v.Interfaces(lv)
	if err != nil {
		return nil, err
	}
	// Isolation : une carte sur le réseau du module, plus celles que d'autres modules demandent (attach:)
	extra := map[string]string{}
	for _, a := range r.l.attachmentsOf(r.y.Module, vm) {
		extra[a.MAC] = a.Network
	}
	own := ifs[:0:0]
	for _, i := range ifs {
		switch {
		case i.Type == "network" && i.Network == r.y.NetworkName():
			own = append(own, i)
		case i.Type == "network" && extra[strings.ToLower(i.MAC)] == i.Network:
		default:
			return nil, fmt.Errorf("%s a une carte hors de %s (%s %s) : refus de continuer", lv, r.y.NetworkName(), i.Type, i.Network+i.MAC)
		}
	}
	ifs = own
	var out []Diff
	cpus, mem, err := v.Resources(lv)
	if err != nil {
		return nil, err
	}
	if cpus != e.CPUs {
		out = append(out, Diff{"cpus", strconv.Itoa(e.CPUs), strconv.Itoa(cpus), true})
	}
	if mem != e.Memory {
		out = append(out, Diff{"memory", fmt.Sprintf("%d Mio", e.Memory), fmt.Sprintf("%d Mio", mem), true})
	}
	if e.MAC != "" && (len(ifs) == 0 || ifs[0].MAC != e.MAC) {
		have := "aucune carte"
		if len(ifs) > 0 {
			have = ifs[0].MAC
		}
		out = append(out, Diff{"mac", e.MAC, have, false})
	}
	if len(ifs) > 1 {
		out = append(out, Diff{"cartes", "1", strconv.Itoa(len(ifs)), false})
	}
	return out, nil
}

// ensureVM crée une VM absente, ou reprend une VM existante (écarts signalés ou appliqués).
func (r *run) ensureVM(vm string, apply bool) error {
	lv := r.libvirtName(vm)
	if r.l.DomState(lv) != virt.StateAbsent {
		return r.adoptVM(vm, apply)
	}
	return r.createVM(vm)
}

func (r *run) adoptVM(vm string, apply bool) error {
	v, lv, e := r.l.Hypervisor(), r.libvirtName(vm), r.eff(vm)
	ifs, _ := v.Interfaces(lv)
	// IP fixe sans MAC dans le YAML : on y écrit celle de la VM existante
	if e.IP > 0 && e.MAC == "" && len(ifs) > 0 {
		if err := r.y.SetVMValue(vm, "mac", ifs[0].MAC); err != nil {
			return err
		}
		if err := r.y.Save(); err != nil {
			return err
		}
		if err := r.reload(); err != nil {
			return err
		}
		r.logf("ok  %s : MAC %s écrite dans %s", vm, ifs[0].MAC, module.FileName)
	}
	diffs, err := r.diffs(vm)
	if err != nil {
		return err
	}
	state := r.l.DomState(lv)
	var fix []Diff
	for _, d := range diffs {
		if d.Fix && apply {
			fix = append(fix, d)
			continue
		}
		hint := ""
		if d.Fix {
			hint = fmt.Sprintf(" → labctl module %s up --apply-changes %s", r.y.Module, vm)
		}
		r.logf("écart %s : %s%s", vm, d, hint)
	}
	if len(fix) > 0 {
		if err := v.SetResources(lv, e.CPUs, e.Memory); err != nil {
			return fmt.Errorf("modification de %s : %w", lv, err)
		}
		r.logf("ok  %s : %d vCPU, %d Mio enregistrés", vm, e.CPUs, e.Memory)
		if state == virt.StateRunning {
			r.logf("==> Redémarrage de %s pour les appliquer", vm)
			if err := r.stopVM(vm, false); err != nil {
				return err
			}
			if err := v.StartDomain(lv); err != nil {
				return err
			}
			if e.Source == nil && len(e.VM.Certificates) > 0 { // certificats vérifiés par SSH juste après
				r.waitSSH(vm, 180)
			}
		}
	}
	if state != virt.StateRunning {
		r.logf("ok  %s existe (%s : labctl module %s start %s)", vm, state, r.y.Module, vm)
	} else {
		r.logf("ok  %s existe", vm)
	}
	if state == virt.StateRunning && len(e.VM.Certificates) > 0 {
		return r.installCerts(vm, false)
	}
	return nil
}

func (r *run) createVM(vm string) (err error) {
	lv, e := r.libvirtName(vm), r.eff(vm)
	if r.l.DomState(GatewayVM) != virt.StateRunning {
		return fmt.Errorf("la passerelle %s doit tourner (DHCP du module) : labctl core start", GatewayVM)
	}
	// IP fixe : la MAC est choisie maintenant et écrite dans le YAML, pour que la réservation DHCP soit stable
	if e.IP > 0 && e.MAC == "" {
		mac := vmdef.RandomMAC()
		if err := r.y.SetVMValue(vm, "mac", mac); err != nil {
			return err
		}
		if err := r.y.Save(); err != nil {
			return err
		}
		if err := r.reload(); err != nil {
			return err
		}
		e = r.eff(vm)
		r.logf("ok  %s : MAC %s écrite dans %s", vm, mac, module.FileName)
	}
	if err := r.applyCore(); err != nil { // réservation DHCP avant le premier démarrage
		return err
	}

	s := vmdef.Spec{
		Name: lv, Image: e.Image, CPUs: e.CPUs, MemoryMiB: e.Memory, DiskGiB: e.Disk, Keyboard: e.Keyboard,
		Nets: []vmdef.NetSpec{{Network: r.y.NetworkName(), MAC: e.MAC}},
		Lab:  r.y.Module, Role: e.Role, NoAgent: !e.Agent,
		HW: vmdef.Hardware{Machine: e.Hardware.Machine, DiskBus: e.Hardware.DiskBus, NICModel: e.Hardware.NICModel,
			Graphics: e.Hardware.Graphics, Video: e.Hardware.Video, OSInfo: e.Hardware.OSInfo, Firmware: e.Hardware.Firmware, SecureBoot: e.Hardware.SecureBoot, TPM: e.Hardware.TPM},
	}
	win := e.VM.Windows
	if e.Source != nil {
		if s.Base, err = r.l.EnsureSource(r.y.Module, vm, e.Source, r.out); err != nil {
			return err
		}
	} else if win != nil {
		if err := r.windowsMedia(vm, e, &s); err != nil {
			return err
		}
		defer func() { // ISO de réponses (mot de passe) : jamais laissé derrière une installation ratée
			if err != nil {
				_ = os.Remove(filepath.Join(r.l.ImagesDir(), "sources", "cache", "answers", lv+".iso"))
			}
		}()
	} else {
		tmp, err := os.MkdirTemp("", "labctl-"+lv+"-")
		if err != nil {
			return err
		}
		defer os.RemoveAll(tmp)
		if err := r.cloudContent(vm, &s, tmp); err != nil {
			return err
		}
	}

	open := false
	closeInternet := func() error {
		if !open {
			return nil
		}
		open = false
		return r.l.Gateway(r.out).InternetOff(r.y.Module, []string{vm})
	}
	if e.Internet == "install" {
		if err := r.l.Gateway(r.out).openForInstall(r.y.Module, vm); err != nil {
			return err
		}
		open = true
		defer func() {
			if cerr := closeInternet(); cerr != nil && err == nil {
				err = cerr
			}
		}()
		// Ctrl+C ou arrêt de labctl pendant l'installation : Internet refermé avant de quitter (et, à défaut, à la
		// prochaine application de la configuration, voir revokeVulnerable)
		defer onInterrupt(func() {
			r.logf("==> Interruption : fermeture d'Internet pour %s", vm)
			if err := closeInternet(); err != nil {
				r.logf("attention: Internet de %s non refermé : %v (labctl core internet off %s %s)", vm, err, r.y.Module, vm)
			}
		})()
	}
	if err := r.l.CreateVM(s, CreateOptions{Wait: e.Source == nil && win == nil, Out: r.out}); err != nil {
		return err
	}
	if e.Source != nil {
		r.waitSourceVM(vm, e)
	}
	if win != nil {
		if err := r.windowsInstall(vm, e); err != nil {
			return err
		}
	}
	if e.Snapshot != "" {
		if err := closeInternet(); err != nil { // l'état de référence est pris Internet fermé
			return err
		}
		if err := r.takeSnapshot(vm, e.Snapshot, "état initial (labctl module up)", true); err != nil {
			return fmt.Errorf("snapshot %s : %w", e.Snapshot, err)
		}
		r.logf("ok  snapshot %s de %s", e.Snapshot, vm)
	}
	return nil
}

// waitSourceVM attend une VM à disque fourni : agent invité (puis clavier), sinon une adresse IP.
func (r *run) waitSourceVM(vm string, e module.Effective) {
	lv := r.libvirtName(vm)
	if !e.Agent {
		r.logf("==> Attente de l'adresse IP de %s...", vm)
		if ip := r.l.WaitVMIP(lv, 180); ip != "" {
			r.logf("ok  %s : %s", vm, ip)
		} else {
			r.logf("attention: %s n'a pas d'IP au bout de 3 min (console : virt-viewer %s)", vm, lv)
		}
		return
	}
	r.logf("==> Attente de l'agent invité de %s...", vm)
	if !r.waitAgent(lv, 240) {
		r.logf("attention: l'agent invité de %s ne répond pas : clavier non réglé (labctl module %s keyboard %s)", vm, r.y.Module, vm)
		return
	}
	if err := r.keyboard(vm, ""); err != nil {
		r.logf("attention: clavier de %s : %v", vm, err)
	}
}

// waitSSH attend que la VM accepte une connexion SSH (timeout en secondes).
func (r *run) waitSSH(vm string, timeout int) bool {
	target := r.l.VMUser() + "@" + r.vmIP(vm)
	for t := 0; t < timeout; t += 3 {
		// « exit 0 » : compris par sh (Linux) comme par PowerShell (Windows)
		if exec.Command("ssh", r.l.sshArgs(target, "exit 0")...).Run() == nil {
			return true
		}
		time.Sleep(3 * time.Second)
	}
	return false
}

func (r *run) waitAgent(lv string, timeout int) bool {
	for t := 0; t < timeout; t += 3 {
		if r.l.Hypervisor().AgentReady(lv) {
			return true
		}
		time.Sleep(3 * time.Second)
	}
	return false
}

// keyboard règle le clavier d'une VM (SSH, sinon agent invité) puis la redémarre pour l'interface graphique.
func (r *run) keyboard(vm, layout string) error {
	lv := r.libvirtName(vm)
	if layout == "" {
		layout = r.l.Keyboard()
	}
	if err := r.l.VMKeyboard(lv, layout, r.out); err != nil {
		return err
	}
	r.logf("==> Redémarrage de %s pour que l'interface graphique prenne le clavier %s", vm, layout)
	if err := r.l.Hypervisor().RebootDomain(lv); err != nil {
		return err
	}
	time.Sleep(10 * time.Second)
	r.waitAgent(lv, 180)
	return nil
}

// cloudContent complète la description d'une VM à image cloud : paquets, fichiers (modèles ${...}
// remplacés), commandes, certificats.
func (r *run) cloudContent(vm string, s *vmdef.Spec, tmp string) error {
	e := r.eff(vm)
	vars := module.VarContext{Module: r.y, VM: vm, Root: r.l.pkiDomain(), Keyboard: r.l.Keyboard(), LabVars: r.l.LabVars()}
	s.Packages = e.VM.Packages
	for i, f := range e.VM.Files {
		src, err := module.SourcePath(r.y.Dir, f.Src) // jamais un fichier de l'hôte hors du module
		if err != nil {
			return fmt.Errorf("%s : %w", vm, err)
		}
		if f.Template {
			b, err := os.ReadFile(src)
			if err != nil {
				return err
			}
			text, err := vars.Expand(string(b))
			if err != nil {
				return fmt.Errorf("%s : %w", f.Src, err)
			}
			src = filepath.Join(tmp, fmt.Sprintf("file-%d", i))
			if err := os.WriteFile(src, []byte(text), 0o600); err != nil {
				return err
			}
		}
		s.Files = append(s.Files, vmdef.FileSpec{Src: src, Dest: f.Dest, Mode: f.Mode})
	}
	for _, c := range e.VM.Run {
		cmd, err := vars.Expand(c)
		if err != nil {
			return err
		}
		s.Run = append(s.Run, cmd)
	}
	for _, c := range e.VM.Certificates {
		crt, key, err := r.ensureCert(vm, c, false)
		if err != nil {
			return err
		}
		s.Files = append(s.Files, vmdef.FileSpec{Src: crt, Dest: c.Cert, Mode: "0644"}, vmdef.FileSpec{Src: key, Dest: c.Key, Mode: "0600"})
	}
	return nil
}
