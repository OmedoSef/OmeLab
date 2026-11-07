package lab

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"labs/labctl/internal/module"
	"labs/labctl/internal/pki"
	"labs/labctl/internal/virt"
)

// Moteur des modules déclaratifs : « labctl module <m> up|status|start|stop|restore|certs|destroy ».
// Tout vient du module.yaml ; rien n'est propre à un module dans le code. Les VMs existantes
// (<module>-<vm>) sont reprises telles quelles : jamais recréées, jamais redémarrées sans --apply-changes.

// run regroupe un module chargé et validé.
type run struct {
	l   *Lab
	y   *module.Module
	out io.Writer
}

// loadRun charge et valide un module ; les erreurs de validation bloquent.
func (l *Lab) loadRun(name string, out io.Writer) (*run, error) {
	if out == nil {
		out = io.Discard
	}
	y, issues, err := l.ValidateModule(name)
	if err != nil {
		return nil, err
	}
	if n := module.Errors(issues); n > 0 {
		var msgs []string
		for _, i := range issues {
			if i.Error {
				msgs = append(msgs, i.String())
			}
		}
		return nil, fmt.Errorf("%s invalide (%d erreur(s)) :\n%s", module.FileName, n, strings.Join(msgs, "\n"))
	}
	// y vient de ValidateModule -> l.Module -> loadYAML, qui a déjà fusionné le câblage local : ne pas réinjecter.
	return &run{l: l, y: y, out: out}, nil
}

// reload relit le module après une écriture dans le YAML.
func (r *run) reload() error {
	y, err := module.Load(r.y.Dir)
	if err != nil {
		return err
	}
	r.l.injectLocalAttach(y)
	r.l.injectLocalConnections(y)
	if err := y.ExpandVars(r.l.pkiDomain(), r.l.Keyboard(), r.l.LabVars()); err != nil {
		return err
	}
	r.y = y
	return nil
}

func (r *run) logf(format string, a ...any) { fmt.Fprintf(r.out, format+"\n", a...) }

func (r *run) libvirtName(vm string) string { return r.y.Module + "-" + vm }
func (r *run) domain() string               { return r.y.Domain(r.l.pkiDomain()) }
func (r *run) bridge() string               { return "virbr-" + r.y.Module }
func (r *run) eff(vm string) module.Effective {
	return r.y.Effective(vm, module.Settings(r.l.Setting))
}

// vmIP renvoie l'IP d'une VM : fixe d'après le YAML, sinon vue par libvirt ou la passerelle.
func (r *run) vmIP(vm string) string {
	if e := r.eff(vm); e.IP > 0 && r.y.Prefix3() != "" {
		return fmt.Sprintf("%s.%d", r.y.Prefix3(), e.IP)
	}
	if DomState(r.libvirtName(vm)) == "running" {
		return r.l.VMIP(r.libvirtName(vm))
	}
	return ""
}

// selectVMs vérifie les VMs demandées ; aucune = toutes, dans l'ordre du fichier.
func (r *run) selectVMs(names []string) ([]string, error) {
	if len(names) == 0 {
		return r.y.VMNames(), nil
	}
	for _, n := range names {
		if _, ok := r.y.VMs[n]; !ok {
			return nil, fmt.Errorf("VM inconnue dans %s : %s (%s)", r.y.Module, n, strings.Join(r.y.VMNames(), ", "))
		}
	}
	return names, nil
}

// ---------------------------------------------------------------- up

// UpOptions règle ModuleUp.
type UpOptions struct {
	ApplyChanges bool // applique les écarts de ressources des VMs existantes (arrêt puis redémarrage)
	Out          io.Writer
}

// ModuleUp crée ce qui manque : réseau, branchement à la passerelle, CA, VMs, certificats, snapshots.
func (l *Lab) ModuleUp(name string, vms []string, o UpOptions) error {
	r, err := l.loadRun(name, o.Out)
	if err != nil {
		return err
	}
	if vms, err = r.selectVMs(vms); err != nil {
		return err
	}
	if err := r.ensureNetwork(); err != nil {
		return err
	}
	if err := r.ensureCA(); err != nil {
		return err
	}
	var failed []string
	for _, vm := range vms {
		if err := r.ensureVM(vm, o.ApplyChanges); err != nil {
			r.logf("ERREUR %s : %v", vm, err)
			failed = append(failed, vm)
		}
	}
	if err := r.ensureAttach(); err != nil {
		r.logf("ERREUR branchements : %v", err)
		failed = append(failed, "attach")
	}
	if err := r.applyCore(); err != nil {
		return err
	}
	if len(failed) > 0 {
		return fmt.Errorf("échec pour : %s", strings.Join(failed, ", "))
	}
	r.logf("ok  module %s à jour (labctl module %s status)", r.y.Module, r.y.Module)
	return nil
}

// ModulePlan affiche ce que « up » ferait, sans rien créer ni modifier (up --dry-run).
func (l *Lab) ModulePlan(name string, vms []string, out io.Writer) error {
	r, err := l.loadRun(name, out)
	if err != nil {
		return err
	}
	if vms, err = r.selectVMs(vms); err != nil {
		return err
	}
	p := func(format string, a ...any) { fmt.Fprintf(out, "  "+format+"\n", a...) }
	fmt.Fprintf(out, "Plan de « labctl %s up » — rien n'est créé (--dry-run) :\n", name)
	v := virt.Shared()

	// Réseau et branchement de la passerelle
	switch {
	case !r.y.Subnet().IsValid():
		if sn, err := r.l.freeSubnet(name); err == nil {
			p("réseau     : sous-réseau %s à choisir (écrit dans le module.yaml), réseau %s à créer", sn, r.y.NetworkName())
		} else {
			p("réseau     : sous-réseau à choisir — %v", err)
		}
	case !v.NetExists(r.y.NetworkName()):
		p("réseau     : %s (%s) à créer", r.y.NetworkName(), r.y.Subnet())
	case DomState(GatewayVM) == "absente":
		p("réseau     : %s présent — pas de passerelle (ni DHCP, ni DNS, ni Internet)", r.y.NetworkName())
	case !r.gatewayAttached():
		p("réseau     : %s présent, passerelle à brancher dessus", r.y.NetworkName())
	default:
		p("réseau     : %s présent, passerelle branchée — à jour", r.y.NetworkName())
	}

	// CA dérivée
	if r.y.PKI != nil {
		if _, err := os.Stat(filepath.Join(r.l.PKIDir(), "sub-ca", r.y.PKI.CA, "ca.crt")); err == nil {
			p("CA         : %s présente", r.y.PKI.CA)
		} else {
			p("CA         : %s à créer", r.y.PKI.CA)
		}
	}

	// VMs
	for _, vm := range vms {
		e := r.eff(vm)
		if DomState(r.libvirtName(vm)) == "absente" {
			src := "image " + e.Image
			if e.Source != nil {
				src = "source éditeur"
			} else if e.VM.Windows != nil {
				src = "installation Windows"
			}
			p("VM %-9s: à créer (%s, %d vCPU, %d Mio, %d Gio)", vm, src, e.CPUs, e.Memory, e.Disk)
			continue
		}
		diffs, err := r.diffs(vm)
		switch {
		case err != nil:
			p("VM %-9s: %v", vm, err)
		case len(diffs) == 0:
			p("VM %-9s: présente, à jour", vm)
		default:
			var ds []string
			for _, d := range diffs {
				ds = append(ds, d.String())
			}
			p("VM %-9s: présente, écarts — %s", vm, strings.Join(ds, " ; "))
		}
	}

	// VMs branchées d'autres modules (attach: + câblage local)
	for _, a := range r.y.Attach {
		owner, avm := a.Split()
		lv := owner + "-" + avm
		plugged := false
		if a.MAC != "" {
			ifs, _ := v.Interfaces(lv)
			for _, it := range ifs {
				plugged = plugged || strings.EqualFold(it.MAC, a.MAC)
			}
		}
		switch {
		case DomState(lv) == "absente":
			p("branchée   : %s absente — à créer dans %s", a.VM, owner)
		case plugged:
			p("branchée   : %s déjà posée (.%d)", a.VM, a.IP)
		default:
			p("branchée   : %s à poser sur %s (.%d)", a.VM, r.y.NetworkName(), a.IP)
		}
	}

	// Passerelle : configuration DHCP/DNS à appliquer
	if DomState(GatewayVM) == "running" {
		if ch, err := r.l.PendingCoreChanges(); err == nil && len(ch) > 0 {
			var files []string
			for _, c := range ch {
				files = append(files, c.File)
			}
			p("passerelle : %d fichier(s) de configuration à appliquer (%s)", len(ch), strings.Join(files, ", "))
		} else {
			p("passerelle : configuration à jour")
		}
	}
	return nil
}

// freeSubnet choisit un /24 libre de la forme 10.N.N.0/24.
func (l *Lab) freeSubnet(name string) (netip.Prefix, error) {
	used := l.ModuleContext(name).Used
	for n := 67; n < 255; n++ {
		p := netip.MustParsePrefix(fmt.Sprintf("10.%d.%d.0/24", n, n))
		if !slices.ContainsFunc(used, func(u module.NetInfo) bool { return u.Subnet.IsValid() && u.Subnet.Overlaps(p) }) {
			return p, nil
		}
	}
	return netip.Prefix{}, errors.New("aucun /24 libre en 10.N.N.0 : écrire network.subnet dans le module")
}

// ensureNetwork : sous-réseau (choisi et écrit au premier up), réseau libvirt isolé, branchement à la passerelle.
func (r *run) ensureNetwork() error {
	if !r.y.Subnet().IsValid() {
		p, err := r.l.freeSubnet(r.y.Module)
		if err != nil {
			return err
		}
		r.y.SetNetwork(r.y.NetworkName(), p.String())
		if err := r.y.Save(); err != nil {
			return err
		}
		if err := r.reload(); err != nil {
			return err
		}
		r.logf("ok  sous-réseau choisi : %s (écrit dans %s)", p, module.FileName)
	}
	v := virt.Shared()
	net, prefix := r.y.NetworkName(), r.y.Prefix3()
	if !v.NetExists(net) {
		xml := fmt.Sprintf("<network>\n  <name>%s</name>\n  <bridge name='%s' stp='on' delay='0'/>\n  <dns enable='no'/>\n  <ip address='%s.1' netmask='255.255.255.0'/>\n</network>\n",
			net, r.bridge(), prefix)
		if err := v.DefineNetwork(xml); err != nil {
			return fmt.Errorf("définition du réseau %s refusée : %w", net, err)
		}
		r.logf("ok  réseau %s défini (%s, isolé ; DHCP, DNS et passerelle : %s en .254)", net, r.y.Subnet(), GatewayVM)
	} else {
		nets, err := v.Networks()
		if err != nil {
			return err
		}
		for _, n := range nets {
			if n.Name != net {
				continue
			}
			if n.Forward != "" {
				return fmt.Errorf("le réseau %s existe mais n'est PAS isolé (forward %s) : refus", net, n.Forward)
			}
			if n.HostIP != prefix+".1" {
				return fmt.Errorf("le réseau %s existe avec l'adresse %s, le module attend %s.1", net, n.HostIP, prefix)
			}
		}
	}
	if !v.NetActive(net) {
		r.logf("==> Démarrage du réseau %s", net)
	}
	if err := v.StartNetwork(net); err != nil {
		return fmt.Errorf("démarrage du réseau %s : %w", net, err)
	}
	if DomState(GatewayVM) == "absente" {
		r.logf("attention: pas de passerelle %s : ni DHCP, ni DNS, ni Internet pour le module (labctl core gw)", GatewayVM)
		return nil
	}
	if r.gatewayAttached() {
		return nil
	}
	return r.l.Gateway(r.out).Attach(r.y.Module, true)
}

// gatewayAttached indique si la passerelle a une carte sur le réseau du module.
func (r *run) gatewayAttached() bool {
	ifs, _ := virt.Shared().Interfaces(GatewayVM)
	return slices.ContainsFunc(ifs, func(i Interface) bool { return i.Network == r.y.NetworkName() })
}

// Interface est une carte réseau de VM.
type Interface = virt.Interface

// ensureCA crée la CA dérivée du module (limitée à son domaine et à son sous-réseau).
func (r *run) ensureCA() error {
	if r.y.PKI == nil {
		return nil
	}
	p := r.l.PKITool(r.out)
	if !p.Initialized() {
		return pki.ErrNotInitialized
	}
	if _, err := os.Stat(filepath.Join(r.l.PKIDir(), "sub-ca", r.y.PKI.CA, "ca.crt")); err == nil {
		return nil
	}
	return p.SubCA(r.y.PKI.CA, r.domain(), r.y.Subnet().String())
}

// applyCore applique sur la passerelle la configuration DHCP/DNS si elle a changé.
func (r *run) applyCore() error {
	if DomState(GatewayVM) != "running" {
		return nil
	}
	changes, err := r.l.PendingCoreChanges()
	if err != nil || len(changes) == 0 {
		return nil
	}
	r.logf("==> Configuration DHCP/DNS de la passerelle")
	return r.l.Gateway(r.out).Apply()
}

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
	v, lv, e := virt.Shared(), r.libvirtName(vm), r.eff(vm)
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
	if DomState(lv) != "absente" {
		return r.adoptVM(vm, apply)
	}
	return r.createVM(vm)
}

func (r *run) adoptVM(vm string, apply bool) error {
	v, lv, e := virt.Shared(), r.libvirtName(vm), r.eff(vm)
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
	state := DomState(lv)
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
		if state == "running" {
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
	if state != "running" {
		r.logf("ok  %s existe (%s : labctl module %s start %s)", vm, state, r.y.Module, vm)
	} else {
		r.logf("ok  %s existe", vm)
	}
	if state == "running" && len(e.VM.Certificates) > 0 {
		return r.installCerts(vm, false)
	}
	return nil
}

func (r *run) createVM(vm string) (err error) {
	lv, e := r.libvirtName(vm), r.eff(vm)
	if DomState(GatewayVM) != "running" {
		return fmt.Errorf("la passerelle %s doit tourner (DHCP du module) : labctl core start", GatewayVM)
	}
	// IP fixe : la MAC est choisie maintenant et écrite dans le YAML, pour que la réservation DHCP soit stable
	if e.IP > 0 && e.MAC == "" {
		mac := RandomMAC()
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

	s := VMSpec{
		Name: lv, Image: e.Image, CPUs: e.CPUs, MemoryMiB: e.Memory, DiskGiB: e.Disk, Keyboard: e.Keyboard,
		Nets: []NetSpec{{Network: r.y.NetworkName(), MAC: e.MAC}},
		Lab:  r.y.Module, Role: e.Role, NoAgent: !e.Agent,
		HW: Hardware{Machine: e.Hardware.Machine, DiskBus: e.Hardware.DiskBus, NICModel: e.Hardware.NICModel,
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
		if err := r.l.Gateway(r.out).InternetOn(r.y.Module, []string{vm}); err != nil {
			return err
		}
		open = true
		defer func() {
			if cerr := closeInternet(); cerr != nil && err == nil {
				err = cerr
			}
		}()
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
	if !waitAgent(lv, 240) {
		r.logf("attention: l'agent invité de %s ne répond pas : clavier non réglé (labctl module %s keyboard %s)", vm, r.y.Module, vm)
		return
	}
	if err := r.keyboard(vm, ""); err != nil {
		r.logf("attention: clavier de %s : %v", vm, err)
	}
}

// waitSSH attend que la VM accepte une connexion SSH (timeout en secondes).
func (r *run) waitSSH(vm string, timeout int) bool {
	target := r.l.Setting("LABS_VM_USER", "lab") + "@" + r.vmIP(vm)
	for t := 0; t < timeout; t += 3 {
		// « exit 0 » : compris par sh (Linux) comme par PowerShell (Windows)
		if exec.Command("ssh", append(append([]string{}, SSHOpts...), "-o", "BatchMode=yes", target, "exit 0")...).Run() == nil {
			return true
		}
		time.Sleep(3 * time.Second)
	}
	return false
}

func waitAgent(lv string, timeout int) bool {
	for t := 0; t < timeout; t += 3 {
		if virt.Shared().AgentReady(lv) {
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
	if err := virt.Shared().RebootDomain(lv); err != nil {
		return err
	}
	time.Sleep(10 * time.Second)
	waitAgent(lv, 180)
	return nil
}

// cloudContent complète la description d'une VM à image cloud : paquets, fichiers (modèles ${...}
// remplacés), commandes, certificats.
func (r *run) cloudContent(vm string, s *VMSpec, tmp string) error {
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
		s.Files = append(s.Files, FileSpec{Src: src, Dest: f.Dest, Mode: f.Mode})
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
		s.Files = append(s.Files, FileSpec{Src: crt, Dest: c.Cert, Mode: "0644"}, FileSpec{Src: key, Dest: c.Key, Mode: "0600"})
	}
	return nil
}

// ---------------------------------------------------------------- certificats

// certWant renvoie les noms DNS et IP attendus d'un certificat déclaré.
func (r *run) certWant(vm string, c module.Cert) (dns, ips []string) {
	dom := r.domain()
	for _, d := range c.DNS {
		if !strings.Contains(d, ".") {
			d += "." + dom
		}
		dns = append(dns, d)
	}
	if c.IP {
		if ip := r.vmIP(vm); ip != "" {
			ips = append(ips, ip)
		}
	}
	return dns, ips
}

// certStale indique pourquoi un certificat émis doit être réémis ("" s'il est bon).
func (r *run) certStale(path string, c module.Cert, dns, ips []string) string {
	leaf, err := pki.ReadCert(path)
	if err != nil {
		return "illisible"
	}
	ca, err := pki.ReadCert(filepath.Join(r.l.PKIDir(), "sub-ca", r.y.PKI.CA, "ca.crt"))
	if err != nil {
		return "CA du module illisible"
	}
	if leaf.CheckSignatureFrom(ca) != nil {
		return "émis par une autre CA (PKI renouvelée)"
	}
	if time.Until(leaf.NotAfter) < 30*24*time.Hour {
		return "expire dans moins de 30 jours"
	}
	have := pki.SANs(leaf)
	want := []string{"DNS:" + c.Name + "." + r.domain()}
	for _, d := range dns {
		want = append(want, "DNS:"+d)
	}
	for _, ip := range ips {
		want = append(want, "IP:"+ip)
	}
	for _, w := range want {
		if !slices.Contains(have, w) {
			return "nom manquant : " + strings.TrimPrefix(strings.TrimPrefix(w, "DNS:"), "IP:")
		}
	}
	return ""
}

// ensureCert émet (ou réémet si renew ou s'il n'est plus valable) un certificat déclaré ; renvoie les
// chemins de la chaîne complète et de la clé.
func (r *run) ensureCert(vm string, c module.Cert, renew bool) (crt, key string, err error) {
	dir := filepath.Join(r.l.PKIDir(), "certs", c.Name)
	crt, key = filepath.Join(dir, c.Name+".fullchain.crt"), filepath.Join(dir, c.Name+".key")
	dns, ips := r.certWant(vm, c)
	if _, err := os.Stat(filepath.Join(dir, c.Name+".crt")); err == nil {
		why := r.certStale(filepath.Join(dir, c.Name+".crt"), c, dns, ips)
		if why == "" && !renew {
			return crt, key, nil
		}
		if why == "" {
			why = "demandé"
		}
		r.logf("==> Réémission du certificat %s (%s)", c.Name, why)
		old := dir + ".old"
		os.RemoveAll(old)
		if err := os.Rename(dir, old); err != nil {
			return "", "", err
		}
		defer func() {
			if err != nil {
				os.RemoveAll(dir)
				_ = os.Rename(old, dir)
			} else {
				os.RemoveAll(old)
			}
		}()
	}
	err = r.l.PKITool(r.out).Issue(pki.IssueOptions{Name: c.Name, SubCA: r.y.PKI.CA, DNS: dns, IPs: ips})
	return crt, key, err
}

// installCerts installe les certificats déclarés d'une VM (par SSH) s'ils diffèrent de ceux en place,
// puis lance la commande reload.
func (r *run) installCerts(vm string, renew bool) error {
	e := r.eff(vm)
	ip := r.vmIP(vm)
	if ip == "" {
		return fmt.Errorf("%s : pas d'IP (VM démarrée ?)", vm)
	}
	target := r.l.Setting("LABS_VM_USER", "lab") + "@" + ip
	ssh := func(script string, stdin io.Reader) ([]byte, error) {
		cmd := exec.Command("ssh", append(append([]string{}, SSHOpts...), "-o", "BatchMode=yes", target, script)...)
		cmd.Stdin = stdin
		return cmd.CombinedOutput()
	}
	vars := module.VarContext{Module: r.y, VM: vm, Root: r.l.pkiDomain(), Keyboard: r.l.Keyboard(), LabVars: r.l.LabVars()}
	for _, c := range e.VM.Certificates {
		crt, key, err := r.ensureCert(vm, c, renew)
		if err != nil {
			return err
		}
		crtB, err1 := os.ReadFile(crt)
		keyB, err2 := os.ReadFile(key)
		if err := errors.Join(err1, err2); err != nil {
			return err
		}
		sum := func(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
		have, err := ssh(fmt.Sprintf("sudo sha256sum %s %s 2>/dev/null | cut -d' ' -f1", shellQuote(c.Cert), shellQuote(c.Key)), nil)
		if err != nil {
			return fmt.Errorf("%s injoignable en SSH (%s) : %s", vm, target, strings.TrimSpace(string(have)))
		}
		if slices.Equal(strings.Fields(string(have)), []string{sum(crtB), sum(keyB)}) {
			r.logf("ok  certificat %s à jour dans %s", c.Name, vm)
			continue
		}
		// Les deux fichiers en un seul envoi, séparés par une ligne repère
		payload := bytes.Join([][]byte{crtB, []byte("--8<-- labctl --8<--\n"), keyB}, nil)
		script := fmt.Sprintf(`set -e; t=$(mktemp -d); csplit -s -f "$t/p" - '/^--8<-- labctl --8<--$/'
sed -i 1d "$t/p01"
sudo install -D -m 0644 "$t/p00" %s; sudo install -D -m 0600 "$t/p01" %s; rm -rf "$t"`, shellQuote(c.Cert), shellQuote(c.Key)) // %q n'est pas un échappement shell
		if b, err := ssh(script, bytes.NewReader(payload)); err != nil {
			return fmt.Errorf("installation du certificat %s : %s", c.Name, strings.TrimSpace(string(b)))
		}
		r.logf("ok  certificat %s installé dans %s", c.Name, vm)
		if c.Reload != "" {
			cmd, err := vars.Expand(c.Reload)
			if err != nil {
				return err
			}
			if b, err := ssh("sudo sh -c "+shellQuote(cmd), nil); err != nil {
				return fmt.Errorf("reload (%s) : %s", cmd, strings.TrimSpace(string(b)))
			}
			r.logf("ok  %s : %s", vm, cmd)
		}
	}
	return nil
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// ModuleCerts (ré)émet et réinstalle les certificats déclarés des VMs démarrées.
func (l *Lab) ModuleCerts(name string, vms []string, renew bool, out io.Writer) error {
	r, err := l.loadRun(name, out)
	if err != nil {
		return err
	}
	if vms, err = r.selectVMs(vms); err != nil {
		return err
	}
	n := 0
	var errs []error
	for _, vm := range vms {
		if len(r.y.VMs[vm].Certificates) == 0 {
			continue
		}
		n++
		if DomState(r.libvirtName(vm)) != "running" {
			for _, c := range r.y.VMs[vm].Certificates {
				if _, _, err := r.ensureCert(vm, c, renew); err != nil {
					errs = append(errs, err)
				}
			}
			r.logf("attention: %s arrêtée : certificats prêts, installés au prochain « up » ou « certs »", vm)
			continue
		}
		if err := r.installCerts(vm, renew); err != nil {
			errs = append(errs, err)
		}
	}
	if n == 0 {
		r.logf("aucun certificat déclaré")
	}
	return errors.Join(errs...)
}

// ---------------------------------------------------------------- cycle de vie

// ModuleStart démarre le réseau puis les VMs (toutes ou celles données).
func (l *Lab) ModuleStart(name string, vms []string, out io.Writer) error {
	r, err := l.loadRun(name, out)
	if err != nil {
		return err
	}
	if vms, err = r.selectVMs(vms); err != nil {
		return err
	}
	v := virt.Shared()
	if err := v.StartNetwork(r.y.NetworkName()); err != nil {
		return fmt.Errorf("réseau %s : %w (labctl module %s up)", r.y.NetworkName(), err, name)
	}
	for _, vm := range vms {
		lv := r.libvirtName(vm)
		switch DomState(lv) {
		case "absente":
			r.logf("attention: %s n'existe pas (labctl module %s up %s)", vm, name, vm)
			continue
		case "running":
			r.logf("ok  %s déjà démarrée", vm)
			continue
		}
		if _, err := r.diffs(vm); err != nil { // isolation vérifiée avant tout démarrage
			return err
		}
		// Réseaux des modules où la VM est branchée : actifs avant son démarrage (sinon libvirt refuse)
		for _, a := range r.l.attachmentsOf(r.y.Module, vm) {
			if v.NetExists(a.Network) {
				if err := v.StartNetwork(a.Network); err != nil {
					return fmt.Errorf("réseau %s (carte de %s) : %w", a.Network, vm, err)
				}
			}
		}
		if err := v.StartDomain(lv); err != nil {
			return fmt.Errorf("%s : %w", vm, err)
		}
		r.logf("ok  %s démarrée", vm)
	}
	return nil
}

// stopVM arrête proprement une VM (ACPI, 90 s) ; force : coupure si elle ne répond pas.
func (r *run) stopVM(vm string, force bool) error {
	v, lv := virt.Shared(), r.libvirtName(vm)
	if DomState(lv) != "running" {
		return nil
	}
	if err := v.ShutdownDomain(lv); err != nil {
		return err
	}
	for t := 0; t < 90; t += 2 {
		if DomState(lv) == "shut off" {
			return nil
		}
		time.Sleep(2 * time.Second)
	}
	if !force {
		return fmt.Errorf("%s ne s'est pas arrêtée en 90 s (fermer sa session, ou stop --force pour couper)", vm)
	}
	r.logf("attention: %s ne répond pas à l'arrêt ACPI : coupure", vm)
	return v.DestroyDomain(lv)
}

// ModuleStop arrête les VMs (toutes ou celles données).
func (l *Lab) ModuleStop(name string, vms []string, force bool, out io.Writer) error {
	r, err := l.loadRun(name, out)
	if err != nil {
		return err
	}
	if vms, err = r.selectVMs(vms); err != nil {
		return err
	}
	var errs []error
	for _, vm := range vms {
		if DomState(r.libvirtName(vm)) != "running" {
			continue
		}
		r.logf("==> Arrêt de %s", vm)
		if err := r.stopVM(vm, force); err != nil {
			errs = append(errs, err)
			continue
		}
		r.logf("ok  %s arrêtée", vm)
	}
	return errors.Join(errs...)
}

// ModuleRestore ramène une VM à un snapshot (défaut : celui de snapshot:).
func (l *Lab) ModuleRestore(name, vm, snap string, out io.Writer) error {
	r, err := l.loadRun(name, out)
	if err != nil {
		return err
	}
	if _, err := r.selectVMs([]string{vm}); err != nil {
		return err
	}
	if snap == "" {
		snap = r.eff(vm).Snapshot
	}
	if snap == "" {
		return fmt.Errorf("pas de snapshot déclaré pour %s : préciser son nom", vm)
	}
	lv := r.libvirtName(vm)
	if !slices.Contains(SnapshotsOf(lv), snap) {
		return fmt.Errorf("pas de snapshot %s sur %s (%s)", snap, vm, strings.Join(SnapshotsOf(lv), ", "))
	}
	if err := virt.Shared().StartNetwork(r.y.NetworkName()); err != nil {
		return err
	}
	// UEFI : snapshot pris VM arrêtée ; la VM est coupée (son état est abandonné de toute façon), restaurée,
	// puis redémarrée si elle tournait
	efiRunning := virt.Shared().IsEFI(lv) && DomState(lv) == "running"
	if efiRunning {
		if err := virt.Shared().DestroyDomain(lv); err != nil {
			return err
		}
	}
	if err := virt.Shared().RevertSnapshot(lv, snap); err != nil {
		return err
	}
	if efiRunning {
		if err := virt.Shared().StartDomain(lv); err != nil {
			return err
		}
	}
	if _, err := r.diffs(vm); err != nil {
		return err
	}
	r.logf("ok  %s restaurée à %s", vm, snap)
	return nil
}

// ModuleSnapshot prend un snapshot d'une VM (replace : remplace un snapshot du même nom).
func (l *Lab) ModuleSnapshot(name, vm, snap string, replace bool, out io.Writer) error {
	r, err := l.loadRun(name, out)
	if err != nil {
		return err
	}
	if _, err := r.selectVMs([]string{vm}); err != nil {
		return err
	}
	if snap == "" {
		snap = r.eff(vm).Snapshot
	}
	if snap == "" {
		snap = "clean"
	}
	lv, v := r.libvirtName(vm), virt.Shared()
	if DomState(lv) == "absente" {
		return fmt.Errorf("%s n'existe pas", vm)
	}
	if slices.Contains(SnapshotsOf(lv), snap) {
		if !replace {
			return fmt.Errorf("le snapshot %s existe déjà sur %s", snap, vm)
		}
		if err := v.DeleteSnapshot(lv, snap); err != nil {
			return err
		}
	}
	if err := r.takeSnapshot(vm, snap, "", false); err != nil {
		return err
	}
	r.logf("ok  snapshot %s de %s", snap, vm)
	return nil
}

// takeSnapshot prend un snapshot interne. Avec un firmware UEFI (pflash), libvirt ne le permet que VM arrêtée :
// la VM est alors arrêtée (ACPI ; coupure si force), photographiée, puis redémarrée.
func (r *run) takeSnapshot(vm, snap, desc string, force bool) error {
	lv, v := r.libvirtName(vm), virt.Shared()
	if !v.IsEFI(lv) || DomState(lv) != "running" {
		return v.CreateSnapshot(lv, snap, desc)
	}
	r.logf("==> %s (UEFI) arrêtée le temps du snapshot", vm)
	if err := r.stopVM(vm, force); err != nil {
		return err
	}
	if err := v.CreateSnapshot(lv, snap, desc); err != nil {
		return err
	}
	return v.StartDomain(lv)
}

// ModuleKeyboard règle le clavier d'une VM (layout vide : celui des labs).
func (l *Lab) ModuleKeyboard(name, vm, layout string, out io.Writer) error {
	r, err := l.loadRun(name, out)
	if err != nil {
		return err
	}
	if _, err := r.selectVMs([]string{vm}); err != nil {
		return err
	}
	if DomState(r.libvirtName(vm)) != "running" {
		return fmt.Errorf("%s n'est pas démarrée", vm)
	}
	return r.keyboard(vm, layout)
}

// ModuleDestroy supprime des VMs (disques compris) ; sans VM : tout le module (VMs, branchement à la
// passerelle, réseau). Le module.yaml, la CA du module et les disques sources téléchargés restent.
func (l *Lab) ModuleDestroy(name string, vms []string, out io.Writer) error {
	r, err := l.loadRun(name, out)
	if err != nil {
		return err
	}
	all := len(vms) == 0
	if vms, err = r.selectVMs(vms); err != nil {
		return err
	}
	var errs []error
	for _, vm := range vms {
		if DomState(r.libvirtName(vm)) == "absente" {
			continue
		}
		if err := l.destroyVM(r.libvirtName(vm), false, true, r.out); err != nil {
			errs = append(errs, err)
		}
	}
	if !all || len(errs) > 0 {
		return errors.Join(errs...)
	}
	v, net := virt.Shared(), r.y.NetworkName()
	if !v.NetExists(net) {
		return nil
	}
	if err := r.detachAll(); err != nil {
		return err
	}
	if DomState(GatewayVM) != "absente" && r.gatewayAttached() {
		if err := r.l.Gateway(r.out).Detach(name); err != nil {
			return err
		}
	}
	if users, _ := v.NetworkUsers(net); len(users) > 0 {
		return fmt.Errorf("réseau %s conservé : encore utilisé par %s", net, strings.Join(users, ", "))
	}
	if err := v.DeleteNetwork(net); err != nil {
		return err
	}
	r.logf("ok  réseau %s supprimé", net)
	return nil
}

// ---------------------------------------------------------------- état

// ModuleVMStatus est l'état d'une VM de module comparé à sa description.
type ModuleVMStatus struct {
	Name      string   `json:"name"`
	Libvirt   string   `json:"libvirt"`
	Role      string   `json:"role"`
	State     string   `json:"state"`
	IP        string   `json:"ip"`
	Snapshot  string   `json:"snapshot"`  // snapshot de référence déclaré
	Snapshots []string `json:"snapshots"` // snapshots existants
	Diffs     []Diff   `json:"diffs"`
	Problems  []string `json:"problems"` // isolation, snapshot manquant...
}

// ModuleStatus est l'état d'un module déclaratif.
type ModuleStatus struct {
	Name      string           `json:"name"`
	Network   string           `json:"network"`
	Subnet    string           `json:"subnet"`
	Domain    string           `json:"domain"`
	NetState  string           `json:"netState"` // absent, inactif, actif
	Gateway   string           `json:"gateway"`  // état de core-gw
	Attached  bool             `json:"attached"` // passerelle branchée sur le réseau du module
	CA        string           `json:"ca"`       // "" si pas de CA déclarée
	CAOK      bool             `json:"caOk"`
	CoreDirty bool             `json:"coreDirty"` // configuration DHCP/DNS à appliquer
	VMs       []ModuleVMStatus `json:"vms"`
}

// ModuleStatus compare un module déclaratif à la réalité (sans rien modifier).
func (l *Lab) ModuleStatus(name string) (*ModuleStatus, error) {
	m, err := l.Module(name)
	if err != nil {
		return nil, err
	}
	if m.YAMLErr != nil {
		return nil, m.YAMLErr
	}
	if m.YAML == nil {
		return nil, fmt.Errorf("le module %s n'a pas de %s", name, module.FileName)
	}
	r := &run{l: l, y: m.YAML, out: io.Discard}
	v := virt.Shared()
	st := &ModuleStatus{Name: name, Network: r.y.NetworkName(), Domain: r.domain(), Gateway: DomState(GatewayVM), NetState: "absent"}
	if p := r.y.Subnet(); p.IsValid() {
		st.Subnet = p.String()
	}
	if v.NetExists(st.Network) {
		st.NetState = "inactif"
		if v.NetActive(st.Network) {
			st.NetState = "actif"
		}
	}
	st.Attached = st.Gateway != "absente" && r.gatewayAttached()
	if r.y.PKI != nil {
		st.CA = r.y.PKI.CA
		_, err := os.Stat(filepath.Join(l.PKIDir(), "sub-ca", st.CA, "ca.crt"))
		st.CAOK = err == nil
	}
	if st.Gateway == "running" {
		if ch, err := l.PendingCoreChanges(); err == nil {
			st.CoreDirty = len(ch) > 0
		}
	}
	for _, vm := range r.y.VMNames() {
		e, lv := r.eff(vm), r.libvirtName(vm)
		vs := ModuleVMStatus{Name: vm, Libvirt: lv, Role: e.Role, State: DomState(lv), Snapshot: e.Snapshot, Diffs: []Diff{}, Problems: []string{}}
		if vs.State != "absente" {
			vs.IP = r.vmIP(vm)
			vs.Snapshots = SnapshotsOf(lv)
			diffs, err := r.diffs(vm)
			if err != nil {
				vs.Problems = append(vs.Problems, err.Error())
			}
			vs.Diffs = append(vs.Diffs, diffs...)
			if e.Snapshot != "" && !slices.Contains(vs.Snapshots, e.Snapshot) {
				vs.Problems = append(vs.Problems, "snapshot "+e.Snapshot+" absent")
			}
			for _, c := range e.VM.Certificates {
				if r.y.PKI == nil {
					continue
				}
				dns, ips := r.certWant(vm, c)
				path := filepath.Join(l.PKIDir(), "certs", c.Name, c.Name+".crt")
				if _, err := os.Stat(path); err != nil {
					vs.Problems = append(vs.Problems, "certificat "+c.Name+" pas encore émis")
				} else if why := r.certStale(path, c, dns, ips); why != "" {
					vs.Problems = append(vs.Problems, "certificat "+c.Name+" à réémettre : "+why)
				}
			}
		} else if e.IP > 0 && r.y.Prefix3() != "" {
			vs.IP = fmt.Sprintf("%s.%d", r.y.Prefix3(), e.IP)
		}
		st.VMs = append(st.VMs, vs)
	}
	return st, nil
}

// ModuleCheck vérifie que l'hôte peut faire tourner le module (KVM, libvirt, outils, clé SSH, PKI,
// passerelle, espace disque). Renvoie une erreur si un point bloque.
func (l *Lab) ModuleCheck(name string, out io.Writer) error {
	r, err := l.loadRun(name, out)
	if err != nil {
		return err
	}
	var tools []string
	for _, vm := range r.y.VMNames() {
		if r.eff(vm).Source != nil {
			tools = []string{"7z"}
		}
	}
	fail := l.HostCheck(out, tools...)
	chk := func(ok bool, what, fix string) {
		if ok {
			r.logf("ok  %s", what)
			return
		}
		r.logf("attention: %s -> %s", what, fix)
		fail++
	}
	if r.y.PKI != nil {
		chk(l.PKITool(io.Discard).Initialized(), "PKI des labs initialisée", "labctl pki init")
	}
	chk(DomState(GatewayVM) != "absente", "passerelle "+GatewayVM+" (DHCP, DNS, Internet des modules)", "labctl core gw")
	if out, err := exec.Command("df", "-BG", "--output=avail", l.VMDir()+"/..").Output(); err == nil {
		var gb int
		fmt.Sscanf(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(string(out)), "Avail")), "%dG", &gb)
		lines := strings.Fields(string(out))
		if len(lines) > 0 {
			fmt.Sscanf(lines[len(lines)-1], "%dG", &gb)
		}
		need := 0
		for _, vm := range r.y.VMNames() {
			need += r.eff(vm).Disk
		}
		chk(gb >= need, fmt.Sprintf("espace libre pour les VMs : %d Gio (jusqu'à %d Gio pour ce module)", gb, need), "libérer de la place")
	}
	if fail > 0 {
		return fmt.Errorf("%d point(s) à corriger", fail)
	}
	r.logf("ok  hôte prêt pour le module %s", name)
	return nil
}
