package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/spf13/cobra"

	"labs/labctl/internal/coreconf"
	"labs/labctl/internal/lab"
)

func coreConfigCmd(l *lab.Lab) *cobra.Command {
	return &cobra.Command{
		Use:   "core-config [DOSSIER]",
		Short: "Génère la configuration de la passerelle (DHCP, DNS, réseau, liens) depuis module.env et core.conf",
		Long: `Génère la configuration de la passerelle core-gw à partir des module.env et core.conf des modules.

Sans DOSSIER : affiche ce qui serait appliqué (pour contrôler avant « labctl core apply »).
Avec DOSSIER : y écrit gwdns/ et networkd/ (utilisé par « core apply », qui les envoie sur la passerelle).`,
		GroupID: "outils",
		Args:    cobra.MaximumNArgs(1),
		ValidArgsFunction: func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
			return nil, cobra.ShellCompDirectiveFilterDirs
		},
		RunE: func(_ *cobra.Command, args []string) error {
			in, err := l.CoreInput()
			if err != nil {
				return err
			}
			files, err := coreconf.Render(in)
			if err != nil {
				return err
			}
			names := make([]string, 0, len(files))
			for n := range files {
				names = append(names, n)
			}
			sort.Strings(names)
			if len(args) == 0 {
				fmt.Printf("# modules branchés sur la passerelle : %v ; DNS amont : %s\n", in.Attached, in.Upstream)
				for _, n := range names {
					fmt.Printf("\n===== %s\n%s", n, files[n])
				}
				return nil
			}
			for _, n := range names {
				p := filepath.Join(args[0], n)
				if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
					return err
				}
				if err := os.WriteFile(p, []byte(files[n]), 0o644); err != nil {
					return err
				}
			}
			return nil
		},
	}
}
