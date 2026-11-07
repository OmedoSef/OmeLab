package lab

import (
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"labs/labctl/internal/virt"
)

// Groupes de VMs : les VMs créées avec « labctl vm create -l LAB » portent lab=LAB dans leur description.
// (Les VMs des modules y sont aussi : lab=<module>.)

// LabGroups compte les VMs par lab.
func LabGroups() (map[string]int, error) {
	doms, err := virt.Shared().Domains()
	if err != nil {
		return nil, err
	}
	out := map[string]int{}
	for _, d := range doms {
		if lb := VMLab(d.Description); lb != "" {
			out[lb]++
		}
	}
	return out, nil
}

// LabVMs renvoie les VMs d'un lab, par nom.
func LabVMs(lab string) ([]string, error) {
	doms, err := virt.Shared().Domains()
	if err != nil {
		return nil, err
	}
	var vms []string
	for _, d := range doms {
		if VMLab(d.Description) == lab {
			vms = append(vms, d.Name)
		}
	}
	sort.Strings(vms)
	if len(vms) == 0 {
		return nil, fmt.Errorf("aucune VM dans le lab %s", lab)
	}
	return vms, nil
}

// LabUp démarre les VMs d'un lab.
func LabUp(lab string, out io.Writer) error {
	vms, err := LabVMs(lab)
	if err != nil {
		return err
	}
	for _, vm := range vms {
		if DomState(vm) == virt.StateRunning {
			fmt.Fprintf(out, "ok  %s déjà démarrée\n", vm)
			continue
		}
		if err := virt.Shared().StartDomain(vm); err != nil {
			return fmt.Errorf("%s : %w", vm, err)
		}
		fmt.Fprintf(out, "ok  %s démarrée\n", vm)
	}
	return nil
}

// LabDown arrête les VMs d'un lab (ACPI ; coupure après 120 s).
func LabDown(lab string, out io.Writer) error {
	vms, err := LabVMs(lab)
	if err != nil {
		return err
	}
	v := virt.Shared()
	for _, vm := range vms {
		if DomState(vm) == virt.StateRunning {
			_ = v.ShutdownDomain(vm)
		}
	}
	for _, vm := range vms {
		for t := 0; t < 120 && DomState(vm) == virt.StateRunning; t += 2 {
			time.Sleep(2 * time.Second)
		}
		if DomState(vm) == virt.StateRunning {
			fmt.Fprintf(out, "attention: %s ne s'arrête pas, arrêt forcé\n", vm)
			_ = v.DestroyDomain(vm)
		}
		fmt.Fprintf(out, "ok  %s arrêtée\n", vm)
	}
	return nil
}

// LabSnapshot prend (revert : restaure) le snapshot snap de chaque VM d'un lab.
func LabSnapshot(lab, snap string, revert bool, out io.Writer) error {
	vms, err := LabVMs(lab)
	if err != nil {
		return err
	}
	var errs []string
	for _, vm := range vms {
		if revert {
			err = virt.Shared().RevertSnapshot(vm, snap)
		} else {
			err = virt.Shared().CreateSnapshot(vm, snap, "")
		}
		if err != nil {
			errs = append(errs, vm+" : "+err.Error())
			continue
		}
		verb := "snapshot " + snap + " pris"
		if revert {
			verb = "restaurée à " + snap
		}
		fmt.Fprintf(out, "ok  %s : %s\n", vm, verb)
	}
	if len(errs) > 0 {
		return fmt.Errorf("%s", strings.Join(errs, "\n"))
	}
	return nil
}
