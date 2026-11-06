package cli

import (
	"errors"
	"fmt"
	"os"
	"strconv"

	"github.com/spf13/cobra"

	"labs/labctl/internal/lab"
)

// coreCmd : la passerelle core-gw, intégrée à labctl (DHCP, DNS, Internet et liens des modules).
func coreCmd(l *lab.Lab) *cobra.Command {
	g := func() *lab.Gateway { return l.Gateway(os.Stdout) }
	c := &cobra.Command{
		Use:     "core",
		Short:   "Passerelle core-gw : Internet, DHCP, DNS et liens entre modules",
		GroupID: "modules",
		Long: `La passerelle core-gw sert tous les modules : DHCP, DNS (<vm>.<domaine du module>, alias), accès Internet
(fermé par défaut et après chaque redémarrage ; jamais vers le LAN ni les réseaux privés) et liens entre
modules (sans lien, aucun module n'en voit un autre). Sa configuration vient des module.yaml.`,
	}
	mods := func(n int) func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
		return func(_ *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
			if len(args) < n {
				return l.ModuleNames(), cobra.ShellCompDirectiveNoFileComp
			}
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
	}
	// MODULE puis ses VMs
	modVMs := func(_ *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
		if len(args) == 0 {
			return l.ModuleNames(), cobra.ShellCompDirectiveNoFileComp
		}
		return l.VMs(args[0]), cobra.ShellCompDirectiveNoFileComp
	}
	simple := func(use, short string, f func() error) *cobra.Command {
		return &cobra.Command{Use: use, Short: short, Args: cobra.NoArgs, ValidArgsFunction: cobra.NoFileCompletions,
			RunE: func(*cobra.Command, []string) error { return f() }}
	}

	c.AddCommand(
		simple("gw", "Crée la passerelle (si besoin), la branche sur chaque module et applique la configuration", func() error { return g().Create() }),
		simple("apply", "Régénère et recharge réseau, DHCP, DNS et pare-feu de la passerelle", func() error { return g().Apply() }),
		simple("update", "Pousse la version courante des fichiers système de la passerelle, puis apply", func() error { return g().Update() }),
		simple("status", "Passerelle, cartes, services, Internet et liens", func() error { return g().PrintStatus() }),
		simple("start", "Démarre la passerelle (Internet fermé pour tous)", func() error { return g().Start() }),
		simple("stop", "Arrête la passerelle (les modules perdent DHCP, DNS et Internet)", func() error { return g().Stop() }),
		simple("links", "Liens entre modules", func() error { return g().PrintLinks() }),
		&cobra.Command{Use: "attach MODULE", Short: "Branche la passerelle sur le réseau d'un module", Args: cobra.ExactArgs(1),
			ValidArgsFunction: mods(1), RunE: func(_ *cobra.Command, a []string) error { return g().Attach(a[0], true) }},
		&cobra.Command{Use: "detach MODULE", Short: "Débranche la passerelle du réseau d'un module", Args: cobra.ExactArgs(1),
			ValidArgsFunction: mods(1), RunE: func(_ *cobra.Command, a []string) error { return g().Detach(a[0]) }},
		coreConfigCmd(l, "config", false),
	)

	var yes bool
	destroy := &cobra.Command{Use: "destroy", Short: "Supprime la passerelle (les modules perdent DHCP, DNS et Internet)", Args: cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			if !yes && !confirmYN("Supprimer la passerelle "+lab.GatewayVM+" ?") {
				return errors.New("abandon")
			}
			return g().Destroy()
		}}
	destroy.Flags().BoolVarP(&yes, "yes", "y", false, "sans confirmation")
	c.AddCommand(destroy, &cobra.Command{
		Use: "ssh [COMMANDE...]", Short: "Shell sur la passerelle", DisableFlagParsing: true,
		RunE: func(_ *cobra.Command, a []string) error { return g().Interactive(a) },
	})

	// portails captifs (connections: … via: portal)
	portalC := &cobra.Command{Use: "portal", Short: "Portails captifs : sessions en cours", Args: cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			list, err := g().PortalSessions()
			if err != nil {
				return err
			}
			printSessions(list)
			return nil
		}}
	portalC.AddCommand(&cobra.Command{Use: "kick PORTAIL IP", Short: "Ferme une session (la source est de nouveau captée)", Args: cobra.ExactArgs(2),
		ValidArgsFunction: cobra.NoFileCompletions,
		RunE: func(_ *cobra.Command, a []string) error {
			id, err := strconv.Atoi(a[0])
			if err != nil {
				return fmt.Errorf("numéro de portail invalide : %q", a[0])
			}
			if err := g().PortalKick(id, a[1]); err != nil {
				return err
			}
			fmt.Printf("ok  session de %s fermée (portail %d)\n", a[1], id)
			return nil
		}})
	c.AddCommand(portalC)

	// internet
	internet := &cobra.Command{Use: "internet", Short: "Accès Internet des modules (fermé par défaut)"}
	internet.AddCommand(
		&cobra.Command{Use: "on MODULE [VM...]", Short: "Ouvre Internet au module entier, ou seulement à ces VMs", Args: cobra.MinimumNArgs(1),
			ValidArgsFunction: modVMs, RunE: func(_ *cobra.Command, a []string) error { return g().InternetOn(a[0], a[1:]) }},
		&cobra.Command{Use: "off MODULE [VM...]", Short: "Referme (tout le module, ou seulement ces VMs)", Args: cobra.MinimumNArgs(1),
			ValidArgsFunction: modVMs, RunE: func(_ *cobra.Command, a []string) error { return g().InternetOff(a[0], a[1:]) }},
		simple("status", "État Internet de chaque module", func() error { return g().PrintInternet() }),
	)
	c.AddCommand(internet)

	// liens
	for _, op := range []string{"link", "unlink"} {
		var both bool
		short := "A peut joindre B et résoudre ses noms (--both : et inversement)"
		if op == "unlink" {
			short = "Retire le lien A -> B (--both : dans les deux sens)"
		}
		cmd := &cobra.Command{Use: op + " A B", Short: short, Args: cobra.ExactArgs(2), ValidArgsFunction: mods(2),
			RunE: func(_ *cobra.Command, a []string) error { return g().Link(a[0], a[1], op == "link", both) }}
		cmd.Flags().BoolVar(&both, "both", false, "dans les deux sens")
		c.AddCommand(cmd)
	}

	// dhcp
	dhcp := &cobra.Command{Use: "dhcp", Short: "Réservations (IP fixes) et baux DHCP des modules"}
	dhcp.AddCommand(
		&cobra.Command{Use: "list [MODULE...]", Short: "Réservations des modules", ValidArgsFunction: mods(99),
			RunE: func(_ *cobra.Command, a []string) error { return g().PrintReservations(a) }},
		&cobra.Command{Use: "leases [MODULE]", Short: "Baux en cours", Args: cobra.MaximumNArgs(1), ValidArgsFunction: mods(1),
			RunE: func(_ *cobra.Command, a []string) error {
				m := ""
				if len(a) == 1 {
					m = a[0]
				}
				return g().PrintLeases(m)
			}},
		&cobra.Command{Use: "add MODULE NOM OCTET MAC", Short: "Réserve SOUS-RÉSEAU.OCTET pour MAC, nommée NOM", Args: cobra.ExactArgs(4),
			ValidArgsFunction: mods(1), RunE: func(_ *cobra.Command, a []string) error {
				o, err := strconv.Atoi(a[2])
				if err != nil {
					return fmt.Errorf("octet invalide : %s", a[2])
				}
				return g().AddReservation(a[0], a[1], o, a[3])
			}},
		&cobra.Command{Use: "del MODULE NOM", Short: "Retire une réservation", Args: cobra.ExactArgs(2),
			ValidArgsFunction: func(_ *cobra.Command, a []string, _ string) ([]string, cobra.ShellCompDirective) {
				if len(a) == 0 {
					return l.ModuleNames(), cobra.ShellCompDirectiveNoFileComp
				}
				if m, err := l.Module(a[0]); err == nil && len(a) == 1 {
					return sortedKeys(m.Conf.Hosts), cobra.ShellCompDirectiveNoFileComp
				}
				return nil, cobra.ShellCompDirectiveNoFileComp
			},
			RunE: func(_ *cobra.Command, a []string) error { return g().DelReservation(a[0], a[1]) }},
		&cobra.Command{Use: "release MAC...", Short: "Libère des baux (fait automatiquement à la suppression d'une VM)", Args: cobra.MinimumNArgs(1),
			RunE: func(_ *cobra.Command, a []string) error { return g().Release(a) }},
	)
	c.AddCommand(dhcp)

	// dns
	dns := &cobra.Command{Use: "dns", Short: "Noms et alias DNS des modules"}
	alias := &cobra.Command{Use: "alias", Short: "Alias <nom>.<domaine du module>"}
	alias.AddCommand(
		&cobra.Command{Use: "add MODULE NOM CIBLE", Short: "NOM.<domaine> -> CIBLE (VM du module ou nom complet)", Args: cobra.ExactArgs(3),
			ValidArgsFunction: func(_ *cobra.Command, a []string, _ string) ([]string, cobra.ShellCompDirective) {
				switch len(a) {
				case 0:
					return l.ModuleNames(), cobra.ShellCompDirectiveNoFileComp
				case 2:
					return l.VMs(a[0]), cobra.ShellCompDirectiveNoFileComp
				}
				return nil, cobra.ShellCompDirectiveNoFileComp
			},
			RunE: func(_ *cobra.Command, a []string) error {
				if a[2] == "" {
					return errors.New("cible vide")
				}
				return g().SetAlias(a[0], a[1], a[2])
			}},
		&cobra.Command{Use: "del MODULE NOM", Short: "Retire un alias", Args: cobra.ExactArgs(2),
			ValidArgsFunction: func(_ *cobra.Command, a []string, _ string) ([]string, cobra.ShellCompDirective) {
				if len(a) == 0 {
					return l.ModuleNames(), cobra.ShellCompDirectiveNoFileComp
				}
				if m, err := l.Module(a[0]); err == nil && len(a) == 1 {
					return sortedKeys(m.Conf.Aliases), cobra.ShellCompDirectiveNoFileComp
				}
				return nil, cobra.ShellCompDirectiveNoFileComp
			},
			RunE: func(_ *cobra.Command, a []string) error { return g().SetAlias(a[0], a[1], "") }},
	)
	dns.AddCommand(&cobra.Command{Use: "list [MODULE...]", Short: "Noms, alias et modules résolus", ValidArgsFunction: mods(99),
		RunE: func(_ *cobra.Command, a []string) error { return g().PrintDNS(a) }}, alias)
	c.AddCommand(dns)
	return c
}
