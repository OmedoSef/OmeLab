package cli

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"labs/labctl/internal/lab"
)

func checkCmd(l *lab.Lab) *cobra.Command {
	return &cobra.Command{
		Use: "check", Short: "Vérifie que l'hôte est prêt (KVM, libvirt, outils, clé SSH, accès au stockage)",
		GroupID: "outils", Args: cobra.NoArgs, ValidArgsFunction: cobra.NoFileCompletions,
		RunE: func(*cobra.Command, []string) error {
			n := l.HostCheck(os.Stdout)
			if vuln := l.VulnerableModules(); len(vuln) > 0 {
				if lab.HostFirewallActive() {
					fmt.Println("ok  pare-feu de l'hôte actif (modules vulnérables : " + strings.Join(vuln, ", ") + ")")
				} else {
					fmt.Println("attention: pare-feu de l'hôte INACTIF alors que des modules vulnérables existent (" + strings.Join(vuln, ", ") +
						") : leurs cibles peuvent joindre les services de ce poste -> sudo labctl hostfw on")
					n++
				}
			}
			// Passerelle démarrée : sa copie de labctl (portails captifs) doit suivre celle de l'hôte après une mise à
			// jour. Vérifié ici et pas dans HostCheck, qui sert aussi avant un « up » qu'il ne doit pas bloquer.
			if cs := l.Core(); cs.State == "running" && cs.Error == "" {
				if cs.LabctlStale {
					fmt.Printf("attention: %s -> labctl core update\n", cs.LabctlNote())
					n++
				} else {
					fmt.Printf("ok  labctl de la passerelle à jour (%s)\n", cs.Labctl)
				}
			}
			if n > 0 {
				return fmt.Errorf("%d point(s) à corriger", n)
			}
			fmt.Println("ok  hôte prêt")
			return nil
		},
	}
}

// labCmd : actions groupées sur les VMs d'un lab (créées avec « labctl vm create -l LAB »).
func labCmd(l *lab.Lab) *cobra.Command {
	c := &cobra.Command{Use: "lab", Short: "Actions groupées sur les VMs d'un lab (vm create -l LAB)", GroupID: "outils"}
	labs := func(_ *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
		if len(args) > 0 {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		g, _ := lab.LabGroups()
		return sortedKeys(g), cobra.ShellCompDirectiveNoFileComp
	}
	one := func(use, short string, f func(string) error) *cobra.Command {
		return &cobra.Command{Use: use + " LAB", Short: short, Args: cobra.ExactArgs(1), ValidArgsFunction: labs,
			RunE: func(_ *cobra.Command, a []string) error { return f(a[0]) }}
	}
	c.AddCommand(&cobra.Command{Use: "list", Short: "Labs existants et nombre de VMs", Args: cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			g, err := lab.LabGroups()
			if err != nil {
				return err
			}
			tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "LAB\tVMS")
			names := sortedKeys(g)
			sort.Strings(names)
			for _, n := range names {
				fmt.Fprintf(tw, "%s\t%d\n", n, g[n])
			}
			return tw.Flush()
		}},
		one("status", "VMs du lab, état et IP", func(name string) error {
			vms, err := lab.LabVMs(name)
			if err != nil {
				return err
			}
			tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "VM\tÉTAT\tIP\tSNAPSHOTS")
			for _, vm := range vms {
				ip, st := "-", lab.DomState(vm)
				if st == "running" {
					if a := l.VMIP(vm); a != "" {
						ip = a
					}
				}
				snaps := strings.Join(lab.SnapshotsOf(vm), ",")
				if snaps == "" {
					snaps = "-"
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", vm, st, ip, snaps)
			}
			return tw.Flush()
		}),
		one("up", "Démarre toutes les VMs du lab", func(n string) error { return lab.LabUp(n, os.Stdout) }),
		one("down", "Arrête toutes les VMs du lab (ACPI, coupure après 120 s)", func(n string) error { return lab.LabDown(n, os.Stdout) }),
		&cobra.Command{Use: "snapshot LAB NOM", Short: "Snapshot de chaque VM du lab", Args: cobra.ExactArgs(2), ValidArgsFunction: labs,
			RunE: func(_ *cobra.Command, a []string) error { return lab.LabSnapshot(a[0], a[1], false, os.Stdout) }},
		&cobra.Command{Use: "revert LAB NOM", Short: "Restaure chaque VM du lab au snapshot NOM", Args: cobra.ExactArgs(2), ValidArgsFunction: labs,
			RunE: func(_ *cobra.Command, a []string) error { return lab.LabSnapshot(a[0], a[1], true, os.Stdout) }},
	)
	var yes bool
	destroy := &cobra.Command{Use: "destroy LAB", Short: "Supprime toutes les VMs du lab", Args: cobra.ExactArgs(1), ValidArgsFunction: labs,
		RunE: func(_ *cobra.Command, a []string) error {
			vms, err := lab.LabVMs(a[0])
			if err != nil {
				return err
			}
			if !yes && !confirmYN(fmt.Sprintf("Supprimer les %d VMs du lab %s (%s) ?", len(vms), a[0], strings.Join(vms, " "))) {
				return errors.New("abandon")
			}
			var errs []error
			for _, vm := range vms {
				if err := l.DestroyVM(vm, false, os.Stdout); err != nil {
					errs = append(errs, err)
				}
			}
			return errors.Join(errs...)
		}}
	destroy.Flags().BoolVarP(&yes, "yes", "y", false, "sans confirmation")
	c.AddCommand(destroy)
	return c
}

func vmKeyboardCmd(l *lab.Lab) *cobra.Command {
	var layout string
	c := &cobra.Command{
		Use:   "keyboard VM...",
		Short: "Règle le clavier de VMs démarrées (SSH, sinon agent invité QEMU)",
		Long: `Règle la disposition du clavier (console et interface graphique) de VMs existantes : par SSH avec
l'utilisateur des labs si possible, sinon par l'agent invité QEMU (VMs sans SSH par clé, ex. Kali). L'interface
graphique prend le nouveau clavier à la prochaine ouverture de session (ou au redémarrage).`,
		Args: cobra.MinimumNArgs(1), ValidArgsFunction: vmNames,
		RunE: func(_ *cobra.Command, args []string) error {
			var errs []error
			for _, vm := range args {
				if err := l.VMKeyboard(vm, layout, os.Stdout); err != nil {
					errs = append(errs, err)
				}
			}
			return errors.Join(errs...)
		},
	}
	c.Flags().StringVarP(&layout, "layout", "l", "", "disposition (fr, us, be...) ; défaut : LABS_KEYBOARD, sinon celle de ce poste")
	return c
}
