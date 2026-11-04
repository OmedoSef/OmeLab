package cli

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"labs/labctl/internal/lab"
)

// engineCmd : commandes d'un module déclaratif, sous « labctl module <m> » et, pour un module sans
// module.sh, directement sous « labctl <m> ».
func engineCmd(l *lab.Lab, name string, group string) *cobra.Command {
	m, _ := l.Module(name)
	short := "module déclaratif"
	if m != nil && m.Description != "" {
		short = m.Description
	}
	c := &cobra.Command{Use: name, Short: short, GroupID: group}
	vmNames := func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
		if m, err := l.Module(name); err == nil && m.YAML != nil {
			return m.YAML.VMNames(), cobra.ShellCompDirectiveNoFileComp
		}
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	oneVM := func(cmd *cobra.Command, args []string, s string) ([]string, cobra.ShellCompDirective) {
		if len(args) > 0 {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		return vmNames(cmd, args, s)
	}

	var apply bool
	up := &cobra.Command{
		Use:   "up [VM...]",
		Short: "Crée ce qui manque : réseau, passerelle, CA, VMs, certificats, snapshots (VMs existantes reprises)",
		Long: `Crée ce qui manque d'après le module.yaml : réseau isolé (sous-réseau choisi et écrit au premier up),
branchement à la passerelle core, CA dérivée, VMs absentes (MAC écrite dans le YAML pour une IP fixe),
certificats, snapshot de référence. Les VMs existantes ne sont jamais recréées : leurs écarts avec le YAML
sont signalés, et appliqués avec --apply-changes (vCPU et mémoire ; arrêt puis redémarrage de la VM).`,
		ValidArgsFunction: vmNames,
		RunE: func(_ *cobra.Command, args []string) error {
			return l.ModuleUp(name, args, lab.UpOptions{ApplyChanges: apply, Out: os.Stdout})
		},
	}
	up.Flags().BoolVar(&apply, "apply-changes", false, "applique les écarts de ressources (redémarre les VMs concernées)")

	status := &cobra.Command{
		Use:   "status",
		Short: "VMs, IP, snapshots et écarts entre le YAML et la réalité",
		Args:  cobra.NoArgs, ValidArgsFunction: cobra.NoFileCompletions,
		RunE: func(*cobra.Command, []string) error {
			st, err := l.ModuleStatus(name)
			if err != nil {
				return err
			}
			printModuleStatus(st)
			return nil
		},
	}

	start := &cobra.Command{
		Use: "start [VM...]", Short: "Démarre le réseau et les VMs (isolation vérifiée)",
		ValidArgsFunction: vmNames,
		RunE:              func(_ *cobra.Command, args []string) error { return l.ModuleStart(name, args, os.Stdout) },
	}
	var force bool
	stop := &cobra.Command{
		Use: "stop [VM...]", Short: "Arrête proprement les VMs (ACPI, 90 s)",
		ValidArgsFunction: vmNames,
		RunE:              func(_ *cobra.Command, args []string) error { return l.ModuleStop(name, args, force, os.Stdout) },
	}
	stop.Flags().BoolVar(&force, "force", false, "coupe une VM qui ne s'arrête pas en 90 s")

	restore := &cobra.Command{
		Use: "restore VM [SNAPSHOT]", Short: "Revient à un snapshot (défaut : celui de snapshot:)",
		Args: cobra.RangeArgs(1, 2),
		ValidArgsFunction: func(cmd *cobra.Command, args []string, s string) ([]string, cobra.ShellCompDirective) {
			if len(args) == 1 {
				return lab.SnapshotsOf(name + "-" + args[0]), cobra.ShellCompDirectiveNoFileComp
			}
			return oneVM(cmd, args, s)
		},
		RunE: func(_ *cobra.Command, args []string) error {
			snap := ""
			if len(args) == 2 {
				snap = args[1]
			}
			return l.ModuleRestore(name, args[0], snap, os.Stdout)
		},
	}

	var renew bool
	certs := &cobra.Command{
		Use:   "certs [VM...]",
		Short: "(Ré)émet et réinstalle les certificats déclarés (après un renouvellement de la PKI)",
		Long: `Émet les certificats déclarés (certificates:) qui manquent, réémet ceux qui ne sont plus valables
(autre CA après un renouvellement de la PKI, noms changés, expiration proche), puis les installe par SSH dans
les VMs démarrées s'ils diffèrent de ceux en place, et lance leur commande reload.`,
		ValidArgsFunction: vmNames,
		RunE: func(_ *cobra.Command, args []string) error {
			return l.ModuleCerts(name, args, renew, os.Stdout)
		},
	}
	certs.Flags().BoolVar(&renew, "renew", false, "réémet même les certificats encore valables")

	snapshot := &cobra.Command{
		Use: "snapshot VM [NOM]", Short: "Snapshot d'une VM (défaut : le nom de snapshot:, sinon clean)",
		Args: cobra.RangeArgs(1, 2), ValidArgsFunction: oneVM,
		RunE: func(_ *cobra.Command, args []string) error {
			snap := ""
			if len(args) == 2 {
				snap = args[1]
			}
			err := l.ModuleSnapshot(name, args[0], snap, false, os.Stdout)
			if err != nil && strings.Contains(err.Error(), "existe déjà") {
				if !confirmYN(err.Error() + " : le remplacer ?") {
					return errors.New("abandon")
				}
				err = l.ModuleSnapshot(name, args[0], snap, true, os.Stdout)
			}
			return err
		},
	}

	keyboard := &cobra.Command{
		Use: "keyboard VM [DISPOSITION]", Short: "Règle le clavier d'une VM (défaut : LABS_KEYBOARD, sinon celui de ce poste)",
		Args: cobra.RangeArgs(1, 2), ValidArgsFunction: oneVM,
		RunE: func(_ *cobra.Command, args []string) error {
			layout := ""
			if len(args) == 2 {
				layout = args[1]
			}
			return l.ModuleKeyboard(name, args[0], layout, os.Stdout)
		},
	}

	ssh := &cobra.Command{
		Use: "ssh VM [COMMANDE...]", Short: "Shell SSH sur une VM (images cloud)",
		Args: cobra.MinimumNArgs(1), ValidArgsFunction: oneVM,
		DisableFlagParsing: true,
		RunE: func(_ *cobra.Command, args []string) error {
			l.Verbose = false
			return l.ExecScript(lookPath("labctl"), append([]string{"vm", "ssh", name + "-" + args[0]}, args[1:]...)...)
		},
	}

	var yes bool
	destroy := &cobra.Command{
		Use:               "destroy [VM...]",
		Short:             "Supprime des VMs et leurs disques ; sans VM : tout le module (le module.yaml reste)",
		ValidArgsFunction: vmNames,
		RunE: func(_ *cobra.Command, args []string) error {
			what := "les VMs " + strings.Join(args, ", ") + " du module " + name + " et leurs disques"
			if len(args) == 0 {
				what = "TOUT le module " + name + " (VMs, disques, réseau ; le module.yaml, la CA et les sources téléchargées restent)"
			}
			if !yes && !confirmYN("Supprimer "+what+" ?") {
				return errors.New("abandon")
			}
			return l.ModuleDestroy(name, args, os.Stdout)
		},
	}
	destroy.Flags().BoolVarP(&yes, "yes", "y", false, "sans confirmation")

	show := &cobra.Command{
		Use: "show", Short: "Valeurs effectives de chaque VM et leur origine", Args: cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error { return showModule(l, name) },
	}
	validate := &cobra.Command{
		Use: "validate", Short: "Contrôle du module.yaml", Args: cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error { return validateModules(l, []string{name}, false) },
	}
	check := &cobra.Command{
		Use: "check", Short: "Vérifie l'hôte : KVM, libvirt, outils, clé SSH, PKI, passerelle, espace disque",
		Args: cobra.NoArgs, ValidArgsFunction: cobra.NoFileCompletions,
		RunE: func(*cobra.Command, []string) error { return l.ModuleCheck(name, os.Stdout) },
	}
	c.AddCommand(check, up, status, start, stop, snapshot, restore, certs, keyboard, ssh, destroy, show, validate)
	return c
}

func lookPath(name string) string {
	if p, err := os.Executable(); err == nil {
		return p
	}
	p, _ := exec.LookPath(name)
	return p
}

func printModuleStatus(st *lab.ModuleStatus) {
	sub := st.Subnet
	if sub == "" {
		sub = "(sous-réseau choisi au premier up)"
	}
	fmt.Printf("module %s — réseau %s %s (%s), domaine %s\n", st.Name, st.Network, sub, st.NetState, st.Domain)
	gw := "passerelle " + lab.GatewayVM + " : " + st.Gateway
	if st.Gateway != "absente" {
		if st.Attached {
			gw += ", branchée"
		} else {
			gw += ", NON branchée (up)"
		}
	}
	if st.CoreDirty {
		gw += ", configuration DHCP/DNS à appliquer (./lab.sh core apply)"
	}
	fmt.Println(gw)
	if st.CA != "" {
		ca := "présente"
		if !st.CAOK {
			ca = "absente (up)"
		}
		fmt.Printf("CA dérivée %s : %s\n", st.CA, ca)
	}
	fmt.Println()
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "VM\tLIBVIRT\tÉTAT\tIP\tSNAPSHOTS")
	for _, v := range st.VMs {
		snaps := strings.Join(v.Snapshots, ",")
		if snaps == "" {
			snaps = "-"
		}
		ip := v.IP
		if ip == "" {
			ip = "-"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", v.Name, v.Libvirt, v.State, ip, snaps)
	}
	tw.Flush()
	ok := true
	for _, v := range st.VMs {
		for _, d := range v.Diffs {
			ok = false
			hint := ""
			if d.Fix {
				hint = fmt.Sprintf("  → labctl module %s up --apply-changes %s", st.Name, v.Name)
			}
			fmt.Printf("écart %s : %s%s\n", v.Name, d, hint)
		}
		for _, p := range v.Problems {
			ok = false
			fmt.Printf("attention %s : %s\n", v.Name, p)
		}
		if v.State == "absente" {
			ok = false
			fmt.Printf("à créer %s : labctl module %s up %s\n", v.Name, st.Name, v.Name)
		}
	}
	if ok {
		fmt.Println("\nconforme au module.yaml")
	}
}
