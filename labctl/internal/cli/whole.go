package cli

import (
	"os"
	"slices"

	"github.com/spf13/cobra"

	"labs/labctl/internal/lab"
)

// Le lab entier d'un coup : « labctl up | start | stop [MODULE...] » (voir lab.UpAll, StartAll, StopAll).

// moduleArgs complète avec les modules du lab pas encore cités.
func moduleArgs(l *lab.Lab) func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
	return func(_ *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
		order, _ := l.ModuleOrder(nil)
		return slices.DeleteFunc(order, func(m string) bool { return slices.Contains(args, m) }), cobra.ShellCompDirectiveNoFileComp
	}
}

func upCmd(l *lab.Lab) *cobra.Command {
	var apply bool
	c := &cobra.Command{
		Use:   "up [MODULE...]",
		Short: "Construit tout le lab : la passerelle si elle manque, puis chaque module (ou ceux donnés)",
		Long: `Construit le lab : la passerelle core-gw si elle manque (démarrée sinon), puis chaque module comme
« labctl <module> up » — réseau, CA, VMs, certificats, snapshots — ce qui manque seulement. Les modules qui
fournissent des VMs branchées ailleurs (attacker) passent avant ceux qui les accueillent. L'échec d'un module
n'arrête pas les autres.`,
		GroupID:           "outils",
		ValidArgsFunction: moduleArgs(l),
		RunE: func(_ *cobra.Command, args []string) error {
			return l.UpAll(args, lab.UpOptions{ApplyChanges: apply, Out: os.Stdout})
		},
	}
	c.Flags().BoolVar(&apply, "apply-changes", false, "applique les écarts de vCPU et de mémoire des VMs existantes (redémarrage)")
	return c
}

func startCmd(l *lab.Lab) *cobra.Command {
	return &cobra.Command{
		Use:               "start [MODULE...]",
		Short:             "Démarre la passerelle puis les VMs de tous les modules (ou de ceux donnés)",
		GroupID:           "outils",
		ValidArgsFunction: moduleArgs(l),
		RunE: func(_ *cobra.Command, args []string) error {
			return l.StartAll(args, os.Stdout)
		},
	}
}

func stopCmd(l *lab.Lab) *cobra.Command {
	var force bool
	c := &cobra.Command{
		Use:   "stop [MODULE...]",
		Short: "Arrête les VMs de tous les modules, puis la passerelle (avec des modules donnés : seulement leurs VMs)",
		Long: `Arrête proprement (ACPI) les VMs des modules, dans l'ordre inverse du démarrage, puis la passerelle quand tout
le lab est arrêté. Avec des modules donnés, la passerelle reste en marche pour les autres.`,
		GroupID:           "outils",
		ValidArgsFunction: moduleArgs(l),
		RunE: func(_ *cobra.Command, args []string) error {
			return l.StopAll(args, force, os.Stdout)
		},
	}
	c.Flags().BoolVar(&force, "force", false, "coupe une VM qui ne s'arrête pas en 90 s")
	return c
}
