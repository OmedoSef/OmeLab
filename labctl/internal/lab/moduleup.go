package lab

import (
	"errors"
	"fmt"
	"io"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"

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
	if err := r.l.prepare(y); err != nil { // même préparation qu'au chargement
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
	if r.l.DomState(r.libvirtName(vm)) == virt.StateRunning {
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
	if r.y.Vulnerable {
		if w := hostfwWarning([]string{name}); w != "" {
			r.logf("attention: %s", w)
		}
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
	v := l.Hypervisor()

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
	case l.DomState(GatewayVM) == virt.StateAbsent:
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
		if l.DomState(r.libvirtName(vm)) == virt.StateAbsent {
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
		case l.DomState(lv) == virt.StateAbsent:
			p("branchée   : %s absente — à créer dans %s", a.VM, owner)
		case plugged:
			p("branchée   : %s déjà posée (.%d)", a.VM, a.IP)
		default:
			p("branchée   : %s à poser sur %s (.%d)", a.VM, r.y.NetworkName(), a.IP)
		}
	}

	// Passerelle : configuration DHCP/DNS à appliquer
	if l.DomState(GatewayVM) == virt.StateRunning {
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
	v := r.l.Hypervisor()
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
	if r.l.DomState(GatewayVM) == virt.StateAbsent {
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
	ifs, _ := r.l.Hypervisor().Interfaces(GatewayVM)
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
	if r.l.DomState(GatewayVM) != virt.StateRunning {
		return nil
	}
	changes, err := r.l.PendingCoreChanges()
	if err != nil || len(changes) == 0 {
		return nil
	}
	r.logf("==> Configuration DHCP/DNS de la passerelle")
	return r.l.Gateway(r.out).Apply()
}
