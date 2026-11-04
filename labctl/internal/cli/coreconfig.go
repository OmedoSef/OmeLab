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

// coreConfigCmd : « labctl core config » (et l'ancien nom « labctl core-config », caché).
func coreConfigCmd(l *lab.Lab, use string, hidden bool) *cobra.Command {
	var diff bool
	c := &cobra.Command{
		Use:    use + " [DOSSIER]",
		Hidden: hidden,
		Short:  "Configuration générée pour la passerelle (DHCP, DNS, réseau, liens) ; --diff : ce qui changerait",
		Long: `Génère la configuration de la passerelle core-gw à partir des module.yaml des modules.

Sans DOSSIER : affiche ce qui serait appliqué (pour contrôler avant « labctl core apply »).
--diff : n'affiche que ce qui changerait par rapport à la configuration installée sur la passerelle.
Avec DOSSIER : y écrit gwdns/ et networkd/ (ce que « core apply » envoie sur la passerelle).`,
		Args: cobra.MaximumNArgs(1),
		ValidArgsFunction: func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
			return nil, cobra.ShellCompDirectiveFilterDirs
		},
		RunE: func(_ *cobra.Command, args []string) error {
			if diff {
				changes, err := l.PendingCoreChanges()
				if err != nil {
					return err
				}
				if len(changes) == 0 {
					fmt.Println("la passerelle est à jour : rien à appliquer")
					return nil
				}
				for _, ch := range changes {
					fmt.Printf("===== %s\n", ch.File)
					for _, d := range ch.Lines {
						fmt.Printf("%s %s\n", d.Op, d.Text)
					}
				}
				fmt.Println("\n(à appliquer avec : labctl core apply)")
				return nil
			}
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
	c.Flags().BoolVar(&diff, "diff", false, "seulement les changements par rapport à la passerelle")
	return c
}
