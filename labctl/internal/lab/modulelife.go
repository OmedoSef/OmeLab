package lab

import (
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"labs/labctl/internal/virt"
)

// Moteur des modules : démarrage, arrêt, snapshots, destruction (voir moduleup.go).

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
	v := l.Hypervisor()
	if err := v.StartNetwork(r.y.NetworkName()); err != nil {
		return fmt.Errorf("réseau %s : %w (labctl module %s up)", r.y.NetworkName(), err, name)
	}
	for _, vm := range vms {
		lv := r.libvirtName(vm)
		switch l.DomState(lv) {
		case virt.StateAbsent:
			r.logf("attention: %s n'existe pas (labctl module %s up %s)", vm, name, vm)
			continue
		case virt.StateRunning:
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
	v, lv := r.l.Hypervisor(), r.libvirtName(vm)
	if r.l.DomState(lv) != virt.StateRunning {
		return nil
	}
	if err := v.ShutdownDomain(lv); err != nil {
		return err
	}
	for t := 0; t < 90; t += 2 {
		if r.l.DomState(lv) == "shut off" {
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
		if l.DomState(r.libvirtName(vm)) != virt.StateRunning {
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
	if !slices.Contains(l.SnapshotsOf(lv), snap) {
		return fmt.Errorf("pas de snapshot %s sur %s (%s)", snap, vm, strings.Join(l.SnapshotsOf(lv), ", "))
	}
	if err := l.Hypervisor().StartNetwork(r.y.NetworkName()); err != nil {
		return err
	}
	// UEFI : snapshot pris VM arrêtée ; la VM est coupée (son état est abandonné de toute façon), restaurée,
	// puis redémarrée si elle tournait
	efiRunning := l.Hypervisor().IsEFI(lv) && l.DomState(lv) == virt.StateRunning
	if efiRunning {
		if err := l.Hypervisor().DestroyDomain(lv); err != nil {
			return err
		}
	}
	if err := l.Hypervisor().RevertSnapshot(lv, snap); err != nil {
		return err
	}
	if efiRunning {
		if err := l.Hypervisor().StartDomain(lv); err != nil {
			return err
		}
	}
	if _, err := r.diffs(vm); err != nil {
		return err
	}
	r.logf("ok  %s restaurée à %s", vm, snap)
	return nil
}

// ModuleRestoreAll ramène chaque VM du module à son snapshot de référence (snapshot:), dans l'ordre du
// module.yaml : un ensemble de VMs qui dépendent les unes des autres (domaine AD…) revient à un état cohérent. Une
// VM sans snapshot déclaré, ou pas encore créée, est laissée telle quelle (signalé).
func (l *Lab) ModuleRestoreAll(name string, out io.Writer) error {
	r, err := l.loadRun(name, out)
	if err != nil {
		return err
	}
	var errs []error
	for _, vm := range r.y.VMNames() {
		switch {
		case l.DomState(r.libvirtName(vm)) == virt.StateAbsent:
			r.logf("attention: %s n'existe pas : ignorée", vm)
		case r.eff(vm).Snapshot == "":
			r.logf("attention: %s n'a pas de snapshot déclaré (snapshot:) : ignorée", vm)
		default:
			if err := l.ModuleRestore(name, vm, "", out); err != nil {
				errs = append(errs, fmt.Errorf("%s : %w", vm, err))
			}
		}
	}
	return errors.Join(errs...)
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
	lv, v := r.libvirtName(vm), l.Hypervisor()
	if l.DomState(lv) == virt.StateAbsent {
		return fmt.Errorf("%s n'existe pas", vm)
	}
	if slices.Contains(l.SnapshotsOf(lv), snap) {
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
	lv, v := r.libvirtName(vm), r.l.Hypervisor()
	if !v.IsEFI(lv) || r.l.DomState(lv) != virt.StateRunning {
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
	if l.DomState(r.libvirtName(vm)) != virt.StateRunning {
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
		if l.DomState(r.libvirtName(vm)) == virt.StateAbsent {
			continue
		}
		if err := l.destroyVM(r.libvirtName(vm), false, true, r.out); err != nil {
			errs = append(errs, err)
		}
	}
	if !all || len(errs) > 0 {
		return errors.Join(errs...)
	}
	v, net := l.Hypervisor(), r.y.NetworkName()
	if !v.NetExists(net) {
		return nil
	}
	if err := r.detachAll(); err != nil {
		return err
	}
	if l.DomState(GatewayVM) != virt.StateAbsent && r.gatewayAttached() {
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
