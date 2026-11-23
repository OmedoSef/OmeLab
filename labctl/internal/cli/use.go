package cli

import (
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"labs/labctl/internal/lab"
)

// useCmd : « labctl use », les labs connus par leur nom et celui piloté par défaut. Fonctionne sans lab (c'est son
// but).
func useCmd() *cobra.Command {
	var as, forget string
	c := &cobra.Command{
		Use:   "use [NOM | DOSSIER]",
		Short: "Labs connus et lab piloté par défaut, depuis n'importe quel dossier : liste, bascule, ajout",
		Long: `Sans argument : les labs connus, celui piloté par défaut et celui piloté ici (et pourquoi).
  labctl use NOM                bascule sur un lab connu : il devient le lab par défaut
  labctl use DOSSIER [--as NOM] ajoute un lab existant (nom : celui du dossier) et bascule dessus
  labctl use --forget NOM       retire un lab de la liste (son dossier n'est pas touché)

Les labs sont mémorisés dans ` + lab.RegistryFile() + ` ; « labctl init » y ajoute chaque lab créé (le
premier devient le lab par défaut). Ordre de recherche du lab piloté : la variable LABS_ROOT, puis le lab qui
contient le dossier courant, puis le lab par défaut.

Un seul lab tourne à la fois sur une machine : tous partagent libvirt, la passerelle core-gw et les noms de VMs.
Avant de basculer vers un lab pour le démarrer, arrêter ou vider l'autre (labctl clear).`,
		GroupID:           "outils",
		Args:              cobra.MaximumNArgs(1),
		ValidArgsFunction: completeLabs,
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			reg, err := lab.LoadRegistry()
			if err != nil {
				return err
			}
			switch {
			case forget != "":
				if err := reg.Forget(forget); err != nil {
					return err
				}
				if err := reg.Save(); err != nil {
					return err
				}
				fmt.Fprintf(out, "ok  lab « %s » retiré de la liste (son dossier est intact)\n", forget)
				return nil
			case len(args) == 1:
				name := args[0]
				if _, known := reg.Labs[name]; !known || as != "" {
					if _, err := os.Stat(name); err != nil && !strings.Contains(name, "/") {
						return fmt.Errorf("lab inconnu : %s (labs connus : %s ; ou donner son dossier)", name, strings.Join(reg.Names(), ", "))
					}
					if name, err = reg.Add(as, name); err != nil {
						return err
					}
				}
				reg.Default = name
				if err := reg.Save(); err != nil {
					return err
				}
				fmt.Fprintf(out, "ok  lab par défaut : « %s » (%s)\n", name, reg.Labs[name])
				return nil
			}
			return printLabs(out, reg)
		},
	}
	c.Flags().StringVar(&as, "as", "", "nom sous lequel enregistrer le lab (défaut : nom du dossier)")
	c.Flags().StringVar(&forget, "forget", "", "retire ce lab de la liste")
	_ = c.RegisterFlagCompletionFunc("forget", completeLabNames)
	return c
}

// printLabs affiche les labs connus (* : par défaut) et le lab piloté ici.
func printLabs(out interface{ Write([]byte) (int, error) }, reg *lab.Registry) error {
	cur, how, curErr := lab.Locate()
	if len(reg.Labs) > 0 {
		tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "\tNOM\tDOSSIER")
		for _, n := range reg.Names() {
			mark := " "
			if n == reg.Default {
				mark = "*"
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\n", mark, n, reg.Labs[n])
		}
		_ = tw.Flush()
		fmt.Fprintln(out, "(* : lab par défaut)")
	} else {
		fmt.Fprintln(out, "Aucun lab enregistré : « labctl init DOSSIER » en crée un, « labctl use DOSSIER » ajoute un lab existant.")
	}
	if curErr != nil {
		return curErr
	}
	name := reg.NameOf(cur.Root)
	if name == "" {
		name = "sans nom"
	}
	fmt.Fprintf(out, "Lab piloté ici : « %s » %s (%s)\n", name, cur.Root, how)
	return nil
}

func completeLabNames(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
	reg, err := lab.LoadRegistry()
	if err != nil {
		return nil, cobra.ShellCompDirectiveError
	}
	return reg.Names(), cobra.ShellCompDirectiveNoFileComp
}

// completeLabs : noms des labs connus, puis dossiers.
func completeLabs(cmd *cobra.Command, args []string, s string) ([]string, cobra.ShellCompDirective) {
	if len(args) > 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	if names, _ := completeLabNames(cmd, args, s); len(names) > 0 && !strings.Contains(s, "/") {
		return names, cobra.ShellCompDirectiveNoFileComp
	}
	return nil, cobra.ShellCompDirectiveFilterDirs
}
