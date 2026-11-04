package cli

import (
	"errors"
	"fmt"
	"os"
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
		Short:   "Modules déclaratifs (module.yaml) : liste, validation, valeurs effectives",
		GroupID: "outils",
		Long:    "Modules déclaratifs : modules/<nom>/module.yaml (voir docs/module-yaml.md).",
	}

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
				format, net, sub, vms := "module.sh", "-", "-", "-"
				switch {
				case m.YAMLErr != nil:
					format = "module.yaml (ILLISIBLE)"
				case m.YAML != nil:
					format = "module.yaml"
					net, vms = m.YAML.NetworkName(), strconv.Itoa(len(m.YAML.VMs))
					if p := m.YAML.Subnet(); p.IsValid() {
						sub = p.String()
					} else {
						sub = "(choisi au 1er up)"
					}
				case m.Env != nil:
					format, net, sub = "module.env", m.Env.Net, m.Env.Subnet+".0/24"
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
		},
	}
	validate.Flags().BoolVarP(&quiet, "quiet", "q", false, "erreurs seulement")
	c.AddCommand(validate)

	c.AddCommand(&cobra.Command{
		Use:   "show MODULE",
		Short: "Valeurs effectives de chaque VM et leur origine (VM, defaults, réglages, intégré)",
		Args:  cobra.ExactArgs(1), ValidArgsFunction: names,
		RunE: func(_ *cobra.Command, args []string) error {
			y, _, err := l.ValidateModule(args[0])
			if err != nil {
				return err
			}
			if y == nil {
				return errors.New("module illisible : labctl module validate " + args[0])
			}
			root := l.Setting("LABS_PKI_DOMAIN", "omedo.lab")
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
		},
	})

	// Pont pour les scripts bash qui lisent encore l'ancien format
	c.AddCommand(&cobra.Command{
		Use:    "env MODULE",
		Short:  "Contenu équivalent à l'ancien module.env (pour les scripts bash)",
		Hidden: true,
		Args:   cobra.ExactArgs(1), ValidArgsFunction: names,
		RunE: func(_ *cobra.Command, args []string) error {
			m, err := l.Module(args[0])
			if err != nil {
				return err
			}
			if m.Env == nil {
				if m.YAMLErr != nil {
					return m.YAMLErr
				}
				return fmt.Errorf("le module %s n'a pas encore de réseau", args[0])
			}
			e := m.Env
			fmt.Printf("MODULE=%s\nNET=%s\nSUBNET=%s\nDOMAIN=%s\n", m.Name, e.Net, e.Subnet, e.Domain)
			fmt.Printf("declare -A HOSTS=(%s)\n", assoc(e.Hosts, func(v int) string { return strconv.Itoa(v) }))
			fmt.Printf("declare -A MACS=(%s)\n", assoc(e.Macs, func(v string) string { return v }))
			fmt.Printf("NO_INTERNET=%q\n", strings.Join(e.NoInternet, " "))
			return nil
		},
	})
	c.AddCommand(&cobra.Command{
		Use:    "conf MODULE",
		Short:  "Contenu équivalent à l'ancien core.conf (pour les scripts bash)",
		Hidden: true,
		Args:   cobra.ExactArgs(1), ValidArgsFunction: names,
		RunE: func(_ *cobra.Command, args []string) error {
			m, err := l.Module(args[0])
			if err != nil {
				return err
			}
			for _, h := range sortedKeys(m.Conf.Hosts) {
				fmt.Printf("host %s %d %s\n", h, m.Conf.Hosts[h], m.Conf.HostMACs[h])
			}
			for _, a := range sortedKeys(m.Conf.Aliases) {
				fmt.Printf("alias %s %s\n", a, m.Conf.Aliases[a])
			}
			for _, k := range m.Conf.Links {
				fmt.Printf("link %s\n", k)
			}
			return nil
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

func assoc[V any](m map[string]V, f func(V) string) string {
	var parts []string
	for _, k := range sortedKeys(m) {
		parts = append(parts, fmt.Sprintf("[%s]=%s", k, f(m[k])))
	}
	return strings.Join(parts, " ")
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
