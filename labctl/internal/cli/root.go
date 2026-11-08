// Package cli définit les commandes de labctl.
package cli

import (
	"fmt"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"labs/labctl/internal/audit"
	"labs/labctl/internal/gui"
	"labs/labctl/internal/lab"
)

// Version est fixée à la compilation (-ldflags "-X labs/labctl/internal/cli.Version=...").
var Version = "dev"

// versionCmd : « labctl version » (fonctionne sans dossier Labs). Le drapeau --version existe aussi.
func versionCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "version",
		Short:   "Affiche la version de labctl",
		GroupID: "outils",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			fmt.Fprintf(cmd.OutOrStdout(), "labctl %s (%s/%s)\n", Version, runtime.GOOS, runtime.GOARCH)
			return nil
		},
	}
}

// Execute construit et lance la commande racine. Chaque commande est journalisée (début, fin et résultat) dans le
// journal d'audit de l'utilisateur (internal/audit) ; une commande remplacée par une autre (vm ssh) n'a que son début.
func Execute() int {
	args := os.Args[1:]
	logged := auditable(args)
	var e audit.Entry
	start := time.Now()
	if logged {
		e = audit.Entry{Origin: audit.Origin(), Args: args}
		if l, err := lab.Find(); err == nil {
			e.Lab = l.Root
		}
		s := e
		s.Event = "start"
		audit.Log(s)
	}
	root := newRoot()
	err := root.Execute()
	if logged {
		ok := err == nil
		e.Event, e.OK, e.Duration = "end", &ok, time.Since(start).Milliseconds()
		if err != nil {
			e.Error = err.Error()
		}
		audit.Log(e)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "erreur :", err)
		return 1
	}
	return 0
}

// auditable : commandes journalisées. Pas la complétion du shell (à chaque Tab), ni l'aide, la version ou le
// service de la passerelle (gw-portal tourne sur core-gw).
func auditable(args []string) bool {
	if len(args) == 0 {
		return false
	}
	switch args[0] {
	case "__complete", "__completeNoDesc", "completion", "help", "version", "gw-portal", "-h", "--help", "--version":
		return false
	}
	return true
}

func newRoot() *cobra.Command {
	lab.Version = Version // comparée à la copie de labctl sur la passerelle (core status, check, GUI)
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

	root.AddGroup(&cobra.Group{ID: "modules", Title: "Modules :"}, &cobra.Group{ID: "outils", Title: "Outils :"})

	// Commandes qui fonctionnent sans dossier Labs : le service de la passerelle (labctl y est copié seul)
	// et « init », qui sert justement à créer un lab là où il n'y en a pas encore.
	root.AddCommand(gwPortalCmd(), initCmd(), versionCmd())

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
	for _, m := range mods {
		if m.HasYAML { // un module = un module.yaml, construit par le moteur de labctl
			root.AddCommand(engineCmd(l, m.Name, "modules"))
		}
	}
	root.AddCommand(checkCmd(l), labCmd(l), modulesCmd(l), vmCmd(l), netCmd(l), imageCmd(l), pkiCmd(l), coreCmd(l), coreConfigCmd(l, "core-config", true), hostfwCmd(), statusCmd(l), clearCmd(l), guiCmd(l, &verbose), completionCmd())
	return root
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
	var noBrowser, app bool
	c := &cobra.Command{
		Use:     "gui",
		Short:   "Ouvre l'interface graphique (servie sur 127.0.0.1 uniquement)",
		GroupID: "outils",
		Args:    cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			l.Verbose = *verbose
			open := gui.OpenBrowser
			if noBrowser {
				open = gui.OpenNone
			} else if app {
				open = gui.OpenApp
			}
			return gui.Serve(l, port, open)
		},
	}
	c.Flags().IntVarP(&port, "port", "p", 8077, "port d'écoute sur 127.0.0.1")
	c.Flags().BoolVar(&noBrowser, "no-browser", false, "ne pas ouvrir le navigateur")
	c.Flags().BoolVar(&app, "app", false, "fenêtre d'application dédiée (Chromium, Chrome, Edge ou Brave) ; la fermer arrête l'interface")
	c.MarkFlagsMutuallyExclusive("no-browser", "app")
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
