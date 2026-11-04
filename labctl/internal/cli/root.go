// Package cli définit les commandes de labctl.
package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"labs/labctl/internal/gui"
	"labs/labctl/internal/lab"
)

// Version est fixée à la compilation (-ldflags "-X labs/labctl/internal/cli.Version=...").
var Version = "dev"

// Execute construit et lance la commande racine.
func Execute() int {
	root := newRoot()
	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "erreur :", err)
		return 1
	}
	return 0
}

func newRoot() *cobra.Command {
	var verbose bool
	root := &cobra.Command{
		Use:           "labctl",
		Short:         "Pilote les labs (modules, passerelle core, VMs) en ligne de commande ou par la GUI",
		Version:       Version,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.PersistentFlags().BoolVarP(&verbose, "verbose", "v", false, "affiche les commandes lancées")
	root.SetHelpCommand(&cobra.Command{Hidden: true})
	root.CompletionOptions.HiddenDefaultCmd = true

	// Les modules sont découverts au lancement : une sous-commande par dossier modules/<nom>/
	l, err := lab.Find()
	if err != nil {
		root.RunE = func(*cobra.Command, []string) error { return err }
		return root
	}
	mods, err := l.Modules()
	if err != nil {
		root.RunE = func(*cobra.Command, []string) error { return err }
		return root
	}
	root.AddGroup(&cobra.Group{ID: "modules", Title: "Modules :"}, &cobra.Group{ID: "outils", Title: "Outils :"})
	for _, m := range mods {
		root.AddCommand(moduleCmd(l, m, &verbose))
	}
	root.AddCommand(modulesCmd(l), vmCmd(l), netCmd(l), imageCmd(l), pkiCmd(l), coreConfigCmd(l), hostfwCmd(), statusCmd(l), guiCmd(l, &verbose), completionCmd())
	return root
}

// moduleCmd transmet tout à « lab.sh <module> ... » ; la complétion suit l'aide du module.
func moduleCmd(l *lab.Lab, m *lab.Module, verbose *bool) *cobra.Command {
	return &cobra.Command{
		Use:                m.Name + " <commande> [arguments]",
		Short:              strings.TrimPrefix(m.Description, "Module "+m.Name+" : "),
		GroupID:            "modules",
		DisableFlagParsing: true, // les options (--both...) appartiennent au module
		RunE: func(cmd *cobra.Command, args []string) error {
			// DisableFlagParsing : -v / --verbose est repris ici à la main
			for len(args) > 0 && (args[0] == "-v" || args[0] == "--verbose") {
				*verbose = true
				args = args[1:]
			}
			l.Verbose = *verbose
			return l.Exec(append([]string{m.Name}, args...)...)
		},
		ValidArgsFunction: func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
			return m.Commands.Complete(m.Name, args, toComplete, l), cobra.ShellCompDirectiveNoFileComp
		},
	}
}

func statusCmd(l *lab.Lab) *cobra.Command {
	return &cobra.Command{
		Use:     "status",
		Short:   "État de la passerelle, des modules et de leurs VMs",
		GroupID: "outils",
		Args:    cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			st, err := l.Status()
			if err != nil {
				return err
			}
			fmt.Printf("Passerelle %s : %s\n", lab.GatewayVM, st.Gateway.State)
			for _, m := range st.Modules {
				links := "aucun"
				if len(m.Links) > 0 {
					links = strings.Join(m.Links, ", ")
				}
				net := "actif"
				if !m.NetActive {
					net = "INACTIF"
				}
				fmt.Printf("\n%s  %s.0/24  %s  (réseau %s %s)\n", m.Name, m.Subnet, m.Domain, m.Net, net)
				fmt.Printf("  Internet : %s    liens vers : %s\n", m.Internet, links)
				for _, vm := range m.VMs {
					tags := []string{}
					if vm.Internet {
						tags = append(tags, "internet")
					}
					if vm.NoInternet {
						tags = append(tags, "jamais d'internet")
					}
					if len(vm.Snapshots) > 0 {
						tags = append(tags, "snapshots : "+strings.Join(vm.Snapshots, ","))
					}
					fmt.Printf("  %-8s %-15s %-10s %s\n", vm.Name, vm.IP, vm.State, strings.Join(tags, " · "))
				}
			}
			return nil
		},
	}
}

func guiCmd(l *lab.Lab, verbose *bool) *cobra.Command {
	var port int
	var noBrowser bool
	c := &cobra.Command{
		Use:     "gui",
		Short:   "Ouvre l'interface graphique (servie sur 127.0.0.1 uniquement)",
		GroupID: "outils",
		Args:    cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			l.Verbose = *verbose
			return gui.Serve(l, port, !noBrowser)
		},
	}
	c.Flags().IntVarP(&port, "port", "p", 8077, "port d'écoute sur 127.0.0.1")
	c.Flags().BoolVar(&noBrowser, "no-browser", false, "ne pas ouvrir le navigateur")
	return c
}

func completionCmd() *cobra.Command {
	return &cobra.Command{
		Use:       "completion bash|zsh|fish",
		Short:     "Script d'autocomplétion (chargé automatiquement par « source ./activate »)",
		GroupID:   "outils",
		ValidArgs: []string{"bash", "zsh", "fish"},
		Args:      cobra.MatchAll(cobra.ExactArgs(1), cobra.OnlyValidArgs),
		RunE: func(cmd *cobra.Command, args []string) error {
			switch args[0] {
			case "bash":
				return cmd.Root().GenBashCompletionV2(os.Stdout, true)
			case "zsh":
				return cmd.Root().GenZshCompletion(os.Stdout)
			default:
				return cmd.Root().GenFishCompletion(os.Stdout, true)
			}
		},
	}
}
