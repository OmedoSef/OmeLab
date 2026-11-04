package cli

import (
	"bufio"
	"fmt"
	"net/netip"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"labs/labctl/internal/lab"
	"labs/labctl/internal/virt"
)

func netNames(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
	nets, _ := virt.Shared().Networks()
	out := make([]string, len(nets))
	for i, n := range nets {
		out[i] = n.Name
	}
	return out, cobra.ShellCompDirectiveNoFileComp
}

func netCmd(l *lab.Lab) *cobra.Command {
	v := virt.Shared()
	c := &cobra.Command{Use: "net", Short: "Réseaux libvirt : liste, création, suppression", GroupID: "outils"}

	c.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "Réseaux avec leur mode, sous-réseau, bridge et VMs connectées",
		Args:  cobra.NoArgs, ValidArgsFunction: cobra.NoFileCompletions,
		RunE: func(*cobra.Command, []string) error {
			nets, err := v.Networks()
			if err != nil {
				return err
			}
			tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "NOM\tETAT\tMODE\tIP HOTE\tBRIDGE\tVMS")
			for _, n := range nets {
				state, mode, ip := "inactif", n.Forward, "-"
				if n.Active {
					state = "actif"
				}
				if mode == "" {
					mode = "isolated"
				}
				if n.HostIP != "" {
					ip = fmt.Sprintf("%s/%d", n.HostIP, n.Prefix)
				}
				users, _ := v.NetworkUsers(n.Name)
				vms := strings.Join(users, " ")
				if vms == "" {
					vms = "-"
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", n.Name, state, mode, ip, n.Bridge, vms)
			}
			return tw.Flush()
		},
	})

	var subnet, mode, domain string
	var noDHCP bool
	create := &cobra.Command{
		Use:   "create NOM -s CIDR [-M nat|isolated|route] [--no-dhcp] [-D DOMAINE]",
		Short: "Crée un réseau libvirt (hôte en .1, DHCP sur la 2e moitié de la plage)",
		Long: `Crée un réseau libvirt, démarré et lancé avec l'hôte.

  nat       accès Internet via l'hôte (défaut)
  isolated  pas de sortie : VMs + hôte uniquement
  route     routé sans NAT

L'hôte prend la 1re adresse ; le DHCP distribue la 2e moitié de la plage (.128-.254 en /24), la 1re moitié
reste libre pour les IP fixes. Les VMs se résolvent par nom dans DOMAINE (défaut : NOM.<domaine des labs>).
Les réseaux des modules sont créés par leur module (labctl <module> up), pas par cette commande.`,
		Example: "  labctl net create demo -s 10.10.0.0/24\n  labctl net create interco -s 10.99.0.0/24 -M route --no-dhcp",
		Args:    cobra.ExactArgs(1), ValidArgsFunction: cobra.NoFileCompletions,
		RunE: func(_ *cobra.Command, args []string) error {
			name := args[0]
			if mode != "nat" && mode != "isolated" && mode != "route" {
				return fmt.Errorf("mode invalide : %s (nat, isolated ou route)", mode)
			}
			pfx, err := netip.ParsePrefix(subnet)
			if err != nil || !pfx.Addr().Is4() || pfx.Bits() < 8 || pfx.Bits() > 29 {
				return fmt.Errorf("sous-réseau IPv4 invalide (préfixe /8 à /29) : %q", subnet)
			}
			pfx = pfx.Masked()
			if v.NetExists(name) {
				return fmt.Errorf("le réseau %s existe déjà", name)
			}
			nets, err := v.Networks()
			if err != nil {
				return err
			}
			for _, n := range nets {
				if a, err := netip.ParseAddr(n.HostIP); err == nil && n.Prefix > 0 {
					other := netip.PrefixFrom(a, n.Prefix).Masked()
					if other.Overlaps(pfx) {
						return fmt.Errorf("sous-réseau déjà utilisé par le réseau %s (%s)", n.Name, other)
					}
				}
			}
			if domain == "" {
				domain = name + "." + l.Setting("LABS_PKI_DOMAIN", "omedo.lab")
			}
			if err := v.DefineNetwork(networkXML(name, mode, domain, pfx, !noDHCP)); err != nil {
				return fmt.Errorf("définition du réseau refusée par libvirt : %w", err)
			}
			if err := v.StartNetwork(name); err != nil {
				return err
			}
			bridge := "?"
			if all, err := v.Networks(); err == nil {
				for _, n := range all {
					if n.Name == name {
						bridge = n.Bridge
					}
				}
			}
			fmt.Fprintf(os.Stderr, "ok  réseau %s (%s) : %s, domaine %s, hôte/passerelle %s, bridge %s\n",
				name, mode, pfx, domain, pfx.Addr().Next(), bridge)
			return nil
		},
	}
	create.Flags().StringVarP(&subnet, "subnet", "s", "", "sous-réseau, ex. 10.10.0.0/24 (obligatoire)")
	create.Flags().StringVarP(&mode, "mode", "M", "nat", "nat, isolated ou route")
	create.Flags().BoolVar(&noDHCP, "no-dhcp", false, "pas de DHCP (IP fixes uniquement)")
	create.Flags().StringVarP(&domain, "domain", "D", "", "domaine DNS local")
	_ = create.MarkFlagRequired("subnet")
	_ = create.RegisterFlagCompletionFunc("mode", cobra.FixedCompletions([]string{"nat", "isolated", "route"}, cobra.ShellCompDirectiveNoFileComp))
	c.AddCommand(create)

	var yes bool
	del := &cobra.Command{
		Use:   "delete [-y] NOM",
		Short: "Supprime un réseau (refusé s'il est utilisé par une VM, et pour default)",
		Args:  cobra.ExactArgs(1), ValidArgsFunction: netNames,
		RunE: func(_ *cobra.Command, args []string) error {
			name := args[0]
			if name == "default" {
				return fmt.Errorf("le réseau default n'est pas supprimé par labctl")
			}
			if !v.NetExists(name) {
				return fmt.Errorf("réseau inconnu : %s", name)
			}
			users, err := v.NetworkUsers(name)
			if err != nil {
				return err
			}
			if len(users) > 0 {
				return fmt.Errorf("réseau utilisé par : %s", strings.Join(users, " "))
			}
			if !yes && !confirmYN(fmt.Sprintf("Supprimer le réseau %s ?", name)) {
				return errAbort
			}
			if err := v.DeleteNetwork(name); err != nil {
				return err
			}
			fmt.Fprintf(os.Stderr, "ok  réseau %s supprimé\n", name)
			return nil
		},
	}
	del.Flags().BoolVarP(&yes, "yes", "y", false, "pas de confirmation")
	c.AddCommand(del)
	return c
}

// networkXML : même définition que l'ancien net-create.sh.
func networkXML(name, mode, domain string, pfx netip.Prefix, dhcp bool) string {
	size := 1 << (32 - pfx.Bits())
	base := pfx.Addr().As4()
	ipAt := func(off int) string {
		n := uint32(base[0])<<24 | uint32(base[1])<<16 | uint32(base[2])<<8 | uint32(base[3])
		n += uint32(off)
		return fmt.Sprintf("%d.%d.%d.%d", n>>24, n>>16&255, n>>8&255, n&255)
	}
	mask := uint32(0xFFFFFFFF) << (32 - pfx.Bits())
	var b strings.Builder
	fmt.Fprintf(&b, "<network>\n  <name>%s</name>\n", name)
	if mode != "isolated" {
		fmt.Fprintf(&b, "  <forward mode='%s'/>\n", mode)
	}
	fmt.Fprintf(&b, "  <domain name='%s' localOnly='yes'/>\n", domain)
	fmt.Fprintf(&b, "  <ip address='%s' netmask='%d.%d.%d.%d'>\n", ipAt(1), mask>>24, mask>>16&255, mask>>8&255, mask&255)
	if dhcp {
		fmt.Fprintf(&b, "    <dhcp><range start='%s' end='%s'/></dhcp>\n", ipAt(size/2), ipAt(size-2))
	}
	b.WriteString("  </ip>\n</network>\n")
	return b.String()
}

// confirmYN pose une question oui/non (non par défaut, et non sans terminal).
func confirmYN(q string) bool {
	if os.Getenv("ASSUME_YES") == "1" { // GUI : la confirmation a déjà été demandée
		return true
	}
	fmt.Fprintf(os.Stderr, "%s [o/N] ", q)
	line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	switch strings.TrimSpace(strings.ToLower(line)) {
	case "o", "oui", "y", "yes":
		return true
	}
	return false
}
