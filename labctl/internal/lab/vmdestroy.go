package lab

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"labs/labctl/internal/virt"
)

// DestroyVM supprime une VM : arrêt, suppression de sa définition (snapshots compris), libération de ses
// baux DHCP sur la passerelle, suppression de son dossier dans le dossier des VMs. Les disques situés
// ailleurs sont conservés. Une VM non créée par les scripts est refusée sauf force.
func (l *Lab) DestroyVM(name string, force bool, out io.Writer) error {
	return l.destroyVM(name, force, false, out)
}

// destroyVM : purge supprime aussi les disques rangés dans un pool libvirt (VMs de module d'avant labctl).
func (l *Lab) destroyVM(name string, force, purge bool, out io.Writer) error {
	v := virt.Shared()
	state := v.State(name)
	if state == virt.StateAbsent {
		return fmt.Errorf("%s n'existe pas", name)
	}
	if !force && !strings.Contains(v.Description(name), LabMarker) {
		return fmt.Errorf("%s n'a pas été créée par les scripts : refusé (--force pour la supprimer quand même ; ses disques hors de %s seront conservés)", name, l.VMDir())
	}
	disks, _ := v.Disks(name)
	ifs, _ := v.Interfaces(name)
	if state != "shut off" {
		if err := v.DestroyDomain(name); err != nil {
			return fmt.Errorf("arrêt de %s : %w", name, err)
		}
	}
	if err := v.UndefineDomain(name); err != nil {
		return fmt.Errorf("suppression de %s : %w", name, err)
	}

	vmDir := filepath.Clean(l.VMDir())
	for _, d := range disks {
		if strings.HasPrefix(filepath.Clean(d), vmDir+string(filepath.Separator)) {
			continue
		}
		if purge {
			if ok, err := v.DeleteVolumeAt(d); ok && err == nil {
				fmt.Fprintf(out, "    disque supprimé : %s\n", d)
				continue
			} else if err != nil {
				fmt.Fprintf(out, "attention: %s non supprimé : %v\n", d, err)
				continue
			}
		}
		fmt.Fprintf(out, "attention: disque conservé (hors de %s) : %s\n", vmDir, d)
	}
	var macs []string
	for _, i := range ifs {
		macs = append(macs, i.MAC)
	}
	l.releaseLeases(macs, out)

	dir := filepath.Join(vmDir, name)
	if filepath.Dir(dir) == vmDir && name != "" && !strings.Contains(name, "/") {
		if err := os.RemoveAll(dir); err != nil {
			return err
		}
	}
	// virt-install créait un pool libvirt par dossier de VM : on le retire aussi
	if pools, err := v.RemovePoolsAt(dir); err == nil && len(pools) > 0 {
		fmt.Fprintf(out, "    pool libvirt retiré : %s\n", strings.Join(pools, ", "))
	}
	fmt.Fprintf(out, "ok  %s supprimée\n", name)
	return nil
}

// releaseLeases libère sur la passerelle core les baux DHCP de ces MAC : l'IP redevient libre et le nom
// DNS disparaît tout de suite. Sans passerelle démarrée : rien à faire.
func (l *Lab) releaseLeases(macs []string, out io.Writer) {
	if len(macs) == 0 || virt.Shared().State(GatewayVM) != virt.StateRunning {
		return
	}
	gw := virt.Shared().IPv4(GatewayVM)
	if gw == "" {
		return
	}
	args := l.sshArgs(l.Setting("LABS_VM_USER", "lab")+"@"+gw, "sudo gwctl release "+strings.Join(macs, " "))
	if err := exec.Command("ssh", args...).Run(); err != nil {
		fmt.Fprintf(out, "attention: baux DHCP non libérés sur %s (%s) : labctl core dhcp release %s\n", GatewayVM, err, strings.Join(macs, " "))
	}
}
