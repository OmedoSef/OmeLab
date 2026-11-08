package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"labs/labctl/internal/lab"
	"labs/labctl/internal/module"
)

// yamlModules liste les modules qui ont un module.yaml.
func yamlModules(l *lab.Lab) []string {
	mods, _ := l.Modules()
	var out []string
	for _, m := range mods {
		if m.HasYAML {
			out = append(out, m.Name)
		}
	}
	return out
}

func modulesCmd(l *lab.Lab) *cobra.Command {
	names := func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
		return yamlModules(l), cobra.ShellCompDirectiveNoFileComp
	}
	c := &cobra.Command{
		Use:     "module",
		Short:   "Modules déclaratifs (module.yaml) : liste, validation, up, status...",
		GroupID: "outils",
		Long:    "Modules déclaratifs : modules/<nom>/module.yaml (voir docs/module-yaml.md).",
	}

	// Une sous-commande par module déclaratif : labctl module <m> up|status|...
	for _, name := range yamlModules(l) {
		c.AddCommand(engineCmd(l, name, ""))
	}

	var schemaWrite bool
	schema := &cobra.Command{
		Use:   "schema",
		Short: "Schéma JSON de module.yaml pour les éditeurs (complétion, descriptions, fautes de frappe)",
		Long: `Affiche le schéma JSON de module.yaml, généré depuis le code de cette version de labctl. Avec --write, l'écrit
à la racine du lab (` + module.SchemaFile + `) : chaque module.yaml y renvoie par sa première ligne
« ` + module.SchemaModeline + ` »
(VS Code avec l'extension YAML de Red Hat, et tout éditeur basé sur yaml-language-server). « labctl init » le pose
déjà ; le réécrire après une mise à jour de labctl.`,
		Args: cobra.NoArgs, ValidArgsFunction: cobra.NoFileCompletions,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !schemaWrite {
				_, err := cmd.OutOrStdout().Write(module.SchemaJSON)
				return err
			}
			p := filepath.Join(l.Root, module.SchemaFile)
			if fi, err := os.Lstat(p); err == nil && fi.Mode()&os.ModeSymlink != 0 {
				fmt.Fprintf(cmd.OutOrStdout(), "%s est un lien (dépôt de développement) : rien à écrire\n", p)
				return nil
			}
			if err := os.WriteFile(p, module.SchemaJSON, 0o644); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "ok  %s\n", p)
			return nil
		},
	}
	schema.Flags().BoolVarP(&schemaWrite, "write", "w", false, "écrire "+module.SchemaFile+" à la racine du lab")
	c.AddCommand(schema)

	c.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "Modules, avec leur réseau et leur nombre de VMs",
		Args:  cobra.NoArgs, ValidArgsFunction: cobra.NoFileCompletions,
		RunE: func(*cobra.Command, []string) error {
			mods, err := l.Modules()
			if err != nil {
				return err
			}
			tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "MODULE\tFORMAT\tRÉSEAU\tSOUS-RÉSEAU\tVMS\tDESCRIPTION")
			for _, m := range mods {
				format, net, sub, vms := "module.yaml", "-", "-", "-"
				switch {
				case m.YAMLErr != nil:
					format = "module.yaml (ILLISIBLE)"
				case m.YAML != nil:
					net, vms = m.YAML.NetworkName(), strconv.Itoa(len(m.YAML.VMs))
					if p := m.YAML.Subnet(); p.IsValid() {
						sub = p.String()
					} else {
						sub = "(choisi au 1er up)"
					}
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", m.Name, format, net, sub, vms, m.Description)
			}
			return tw.Flush()
		},
	})

	var quiet bool
	validate := &cobra.Command{
		Use:   "validate [MODULE...]",
		Short: "Contrôle des module.yaml sans rien créer (code de sortie 1 en cas d'erreur)",
		Long: `Contrôle des module.yaml sans rien créer : structure (clés inconnues, types), cohérence (IP, MAC,
alias, liens, certificats, variables ${...}), fichiers référencés, conflits avec les autres modules et les
réseaux libvirt. Sans argument : tous les modules déclaratifs.`,
		ValidArgsFunction: names,
		RunE: func(_ *cobra.Command, args []string) error {
			if len(args) == 0 {
				args = yamlModules(l)
			}
			return validateModules(l, args, quiet)
		},
	}
	validate.Flags().BoolVarP(&quiet, "quiet", "q", false, "erreurs seulement")
	c.AddCommand(validate)

	c.AddCommand(&cobra.Command{
		Use:   "show MODULE",
		Short: "Valeurs effectives de chaque VM et leur origine (VM, defaults, réglages, intégré)",
		Args:  cobra.ExactArgs(1), ValidArgsFunction: names,
		RunE: func(_ *cobra.Command, args []string) error {
			return showModule(l, args[0])
		},
	})

	c.AddCommand(&cobra.Command{
		Use:   "edit MODULE host-add NOM OCTET MAC | host-del NOM | alias-add NOM CIBLE | alias-del NOM | link AUTRE | unlink AUTRE",
		Short: "Modifie réservations, alias et liens d'un module (mêmes validations que la GUI)",
		Args:  cobra.MinimumNArgs(3), ValidArgsFunction: names,
		RunE: func(_ *cobra.Command, args []string) error {
			m, op, a := args[0], args[1], args[2:]
			need := func(n int) error {
				if len(a) != n {
					return fmt.Errorf("%s : %d argument(s) attendu(s)", op, n)
				}
				return nil
			}
			switch op {
			case "host-add":
				if err := need(3); err != nil {
					return err
				}
				octet, err := strconv.Atoi(a[1])
				if err != nil {
					return fmt.Errorf("octet invalide : %s", a[1])
				}
				return l.ConfAddHost(m, a[0], octet, a[2])
			case "host-del":
				if err := need(1); err != nil {
					return err
				}
				return l.ConfDelHost(m, a[0])
			case "alias-add":
				if err := need(2); err != nil {
					return err
				}
				return l.ConfAddAlias(m, a[0], a[1])
			case "alias-del":
				if err := need(1); err != nil {
					return err
				}
				return l.ConfDelAlias(m, a[0])
			case "link", "unlink":
				if err := need(1); err != nil {
					return err
				}
				return l.ConfLink(m, a[0], op == "link")
			}
			return fmt.Errorf("opération inconnue : %s", op)
		},
	})
	return c
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// validateModules affiche les problèmes des module.yaml donnés ; erreur s'il y en a au moins une.
func validateModules(l *lab.Lab, args []string, quiet bool) error {
	errs, warns := 0, 0
	for _, name := range args {
		_, issues, err := l.ValidateModule(name)
		if err != nil {
			return err
		}
		e := module.Errors(issues)
		errs, warns = errs+e, warns+len(issues)-e
		if quiet && len(issues) == 0 {
			continue
		}
		fmt.Printf("modules/%s/%s\n", name, module.FileName)
		for _, i := range issues {
			if quiet && !i.Error {
				continue
			}
			fmt.Println(i)
		}
		if len(issues) == 0 {
			fmt.Println("  ok")
		}
	}
	fmt.Printf("%d erreur(s), %d avertissement(s)\n", errs, warns)
	if errs > 0 {
		return errors.New("configuration invalide")
	}
	return nil
}

// showModule affiche les valeurs effectives des VMs d'un module et leur origine.
func showModule(l *lab.Lab, name string) error {
	y, _, err := l.ValidateModule(name)
	if err != nil {
		return err
	}
	if y == nil {
		return errors.New("module illisible : labctl module validate " + name)
	}
	root := l.RootDomain()
	sub := "(choisi au premier up)"
	if p := y.Subnet(); p.IsValid() {
		sub = p.String()
	}
	fmt.Printf("module %s — réseau %s, %s, domaine %s\n", y.Module, y.NetworkName(), sub, y.Domain(root))
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	for _, name := range y.VMNames() {
		e := y.Effective(name, l.Setting)
		fmt.Fprintf(tw, "\n%s\t\t\n", name)
		row := func(field, value string) {
			origin := e.Origins[field]
			if origin == "" {
				origin = module.FromVM
			}
			fmt.Fprintf(tw, "  %s\t%s\t(%s)\n", field, value, origin)
		}
		ip := "DHCP libre (.100-.199)"
		if e.IP > 0 {
			ip = fmt.Sprintf("%s.%d", y.Prefix3(), e.IP)
		}
		fmt.Fprintf(tw, "  ip\t%s\t\n", ip)
		if e.Source != nil {
			src := e.Source.URL
			if src == "" {
				src = e.Source.Sums + " → " + e.Source.File
			}
			fmt.Fprintf(tw, "  source\t%s\t\n", src)
		} else {
			row("image", e.Image)
		}
		row("cpus", strconv.Itoa(e.CPUs))
		row("memory", fmt.Sprintf("%d Mio", e.Memory))
		row("disk", fmt.Sprintf("%d Gio", e.Disk))
		row("internet", e.Internet)
		snap := e.Snapshot
		if snap == "" {
			snap = "-"
		}
		row("snapshot", snap)
		hw := []string{}
		for _, f := range []struct{ k, v string }{{"machine", e.Hardware.Machine}, {"disk_bus", e.Hardware.DiskBus},
			{"nic_model", e.Hardware.NICModel}, {"graphics", e.Hardware.Graphics}, {"video", e.Hardware.Video}} {
			hw = append(hw, f.k+"="+f.v)
		}
		fmt.Fprintf(tw, "  hardware\t%s\t\n", strings.Join(hw, " "))
	}
	return tw.Flush()
}
