package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"labs/labctl/internal/lab"
)

// useCmd : « labctl use [DOSSIER] », lab par défaut de l'utilisateur. Fonctionne sans lab (c'est son but).
func useCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "use [DOSSIER]",
		Short: "Lab piloté par défaut, depuis n'importe quel dossier : l'affiche, ou le choisit",
		Long: `Sans argument : affiche le lab que labctl pilote ici, et pourquoi. Avec un dossier : en fait le lab par défaut,
piloté depuis n'importe quel dossier (mémorisé dans ` + lab.DefaultFile() + `).

Ordre de recherche : la variable LABS_ROOT, puis le lab qui contient le dossier courant (travailler dans un autre
lab, c'est s'y placer), puis le lab par défaut. « labctl init » fait du premier lab créé le lab par défaut.`,
		GroupID: "outils",
		Args:    cobra.MaximumNArgs(1),
		ValidArgsFunction: func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
			return nil, cobra.ShellCompDirectiveFilterDirs
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			if len(args) == 1 {
				abs, err := lab.SetDefault(args[0])
				if err != nil {
					return err
				}
				fmt.Fprintf(out, "ok  lab par défaut : %s\n", abs)
				return nil
			}
			l, how, err := lab.Locate()
			if err != nil {
				return err
			}
			fmt.Fprintf(out, "Lab piloté ici : %s (%s)\n", l.Root, how)
			if def, err := lab.DefaultRoot(); err == nil && def != "" && def != l.Root {
				fmt.Fprintf(out, "Lab par défaut : %s\n", def)
			}
			return nil
		},
	}
}
