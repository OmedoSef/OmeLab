package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"labs/labctl/internal/lab"
	"labs/labctl/internal/pki"
	"labs/labctl/internal/virt"
)

func newPKI(l *lab.Lab) *pki.PKI {
	return l.PKITool(os.Stderr) // mêmes réglages que la GUI (organisation, domaine racine, dossier)
}

func pkiCmd(l *lab.Lab) *cobra.Command {
	p := newPKI(l)
	root := &cobra.Command{
		Use:     "pki",
		Short:   "PKI des labs : CA racine, intermédiaire, CA dérivées, certificats",
		GroupID: "outils",
		Long: `PKI des labs : CA racine -> CA intermédiaire -> certificats, et CA dérivées (une par module en
général) signées par la racine et limitées à un domaine et un sous-réseau.

Les VMs créées par les scripts font confiance à la CA racine. Fichiers dans ` + p.Dir + `
(clés non chiffrées : ne pas versionner ce dossier).`,
	}
	certNames := func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
		return p.CertNames(), cobra.ShellCompDirectiveNoFileComp
	}
	noFiles := cobra.NoFileCompletions

	root.AddCommand(&cobra.Command{
		Use:   "init",
		Short: "Crée la CA racine (20 ans) et la CA intermédiaire (10 ans)",
		Args:  cobra.NoArgs, ValidArgsFunction: noFiles,
		RunE: func(*cobra.Command, []string) error { return p.Init() },
	})

	var subDomain, subNet string
	sub := &cobra.Command{
		Use:     "sub-ca NOM -D DOMAINE [-s CIDR]",
		Short:   "Crée une CA dérivée limitée à un domaine (et à un sous-réseau pour les IP)",
		Example: "  labctl pki sub-ca pentest -D pentest.omelab.internal -s 10.66.66.0/24",
		Args:    cobra.ExactArgs(1), ValidArgsFunction: noFiles,
		RunE: func(_ *cobra.Command, args []string) error { return p.SubCA(args[0], subDomain, subNet) },
	}
	sub.Flags().StringVarP(&subDomain, "domain", "D", "", "domaine autorisé, sous-domaines compris (obligatoire)")
	sub.Flags().StringVarP(&subNet, "subnet", "s", "", "sous-réseau autorisé pour les IP (sinon aucune IP)")
	_ = sub.MarkFlagRequired("domain")
	root.AddCommand(sub)

	var o pki.IssueOptions
	issue := &cobra.Command{
		Use:   "issue NOM [options]",
		Short: "Émet un certificat (serveur + client par défaut)",
		Long: `Émet un certificat signé par la CA intermédiaire, ou par une CA dérivée (-C).

SAN : NOM et NOM.<domaine> si NOM n'est pas un nom complet ; sous une CA dérivée, le nom court est omis
(il serait refusé par sa contrainte de nom). Le certificat est vérifié avant d'être conservé.`,
		Example: `  labctl pki issue web -C pentest -i 10.66.66.30 -d dvwa.pentest.omelab.internal
  labctl pki issue ingress -D k8s.omelab.internal -d '*.apps.k8s.omelab.internal'
  labctl pki issue alice --client --pfx`,
		Args: cobra.ExactArgs(1), ValidArgsFunction: noFiles,
		RunE: func(_ *cobra.Command, args []string) error {
			o.Name = args[0]
			return p.Issue(o)
		},
	}
	issue.Flags().StringVarP(&o.SubCA, "ca", "C", "", "CA dérivée émettrice (défaut : intermédiaire)")
	issue.Flags().StringVarP(&o.Domain, "domain", "D", "", "domaine ajouté au nom court (défaut : celui de la CA dérivée, ou "+p.Domain+")")
	issue.Flags().StringSliceVarP(&o.DNS, "dns", "d", nil, "noms DNS supplémentaires, séparés par des virgules (wildcards acceptés)")
	issue.Flags().StringSliceVarP(&o.IPs, "ip", "i", nil, "adresses IP, séparées par des virgules")
	issue.Flags().BoolVar(&o.Client, "client", false, "certificat client seulement")
	issue.Flags().IntVar(&o.Days, "days", pki.DefaultDays, "validité en jours")
	issue.Flags().BoolVar(&o.PFX, "pfx", false, "génère aussi un .p12 (mot de passe vide)")
	_ = issue.RegisterFlagCompletionFunc("ca", func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
		return p.SubCANames(), cobra.ShellCompDirectiveNoFileComp
	})
	root.AddCommand(issue)

	root.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "CA dérivées et certificats émis, avec leur expiration",
		Args:  cobra.NoArgs, ValidArgsFunction: noFiles,
		RunE: func(*cobra.Command, []string) error { return p.List(os.Stdout) },
	})
	root.AddCommand(&cobra.Command{
		Use:   "show NOM",
		Short: "Détail d'un certificat émis",
		Args:  cobra.ExactArgs(1), ValidArgsFunction: certNames,
		RunE: func(_ *cobra.Command, args []string) error { return p.Show(os.Stdout, args[0]) },
	})
	var interception bool
	trustVM := &cobra.Command{
		Use:   "trust-vm VM...",
		Short: "Installe la CA racine (ou --interception : l'autorité du proxy) dans des VMs existantes (SSH)",
		Args:  cobra.MinimumNArgs(1),
		ValidArgsFunction: func(_ *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
			return without(l.AllVMNames(), args), cobra.ShellCompDirectiveNoFileComp
		},
		RunE: func(_ *cobra.Command, args []string) error {
			if interception {
				return p.TrustInterceptionVM(l.Remote(), args...)
			}
			return p.TrustVM(l.Remote(), args...)
		},
	}
	trustVM.Flags().BoolVar(&interception, "interception", false, "autorité d'interception du proxy (connexions via: proxy, mode intercept) au lieu de la racine")
	root.AddCommand(trustVM)

	root.AddCommand(&cobra.Command{
		Use:   "interception",
		Short: "Crée (si besoin) l'autorité d'interception du proxy et indique son certificat",
		Long: `L'autorité d'interception est une racine à part (hors de la chaîne racine → intermédiaire) dont le proxy de la
passerelle se sert en mode intercept pour signer à la volée. Elle est créée automatiquement au premier montage
du proxy ; cette commande la crée à l'avance pour pouvoir la télécharger et l'importer (navigateur, VMs).
La poser dans une VM : labctl pki trust-vm --interception VM.`,
		Args: cobra.NoArgs, ValidArgsFunction: noFiles,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if _, _, err := p.InterceptionCA(); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Autorité d'interception prête : %s\n", filepath.Join(p.Dir, "interception", "interception-ca.crt"))
			return nil
		},
	})
	root.AddCommand(&cobra.Command{
		Use:   "deploy VM NOM [DOSSIER]",
		Short: "Copie clé, certificat, chaîne et racine dans la VM (défaut /etc/ssl/lab)",
		Args:  cobra.RangeArgs(2, 3),
		ValidArgsFunction: func(_ *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
			switch len(args) {
			case 0:
				return l.AllVMNames(), cobra.ShellCompDirectiveNoFileComp
			case 1:
				return p.CertNames(), cobra.ShellCompDirectiveNoFileComp
			}
			return nil, cobra.ShellCompDirectiveNoFileComp
		},
		RunE: func(_ *cobra.Command, args []string) error {
			dest := ""
			if len(args) == 3 {
				dest = args[2]
			}
			return p.Deploy(l.Remote(), args[0], args[1], dest)
		},
	})
	root.AddCommand(renewCmd(l, p))
	root.AddCommand(&cobra.Command{
		Use:   "archives",
		Short: "Archives de la PKI créées avant chaque renouvellement",
		Args:  cobra.NoArgs, ValidArgsFunction: noFiles,
		RunE: func(*cobra.Command, []string) error {
			for _, a := range p.Archives() {
				fmt.Println(filepath.Join(p.Dir, "archive", a))
			}
			return nil
		},
	})
	root.AddCommand(&cobra.Command{
		Use:   "trust-host",
		Short: "Installe la CA racine sur cet hôte (sudo)",
		Args:  cobra.NoArgs, ValidArgsFunction: noFiles,
		RunE: func(*cobra.Command, []string) error {
			if os.Getenv("LABCTL_FROM_GUI") != "" { // sudo dans un terminal seulement (voir la GUI)
				return errors.New("pki trust-host demande sudo : à lancer dans un terminal")
			}
			return p.TrustHost()
		},
	})
	return root
}

// renewCmd : renouvellement de la chaîne (intermédiaire, CA dérivée ou racine).
func renewCmd(l *lab.Lab, p *pki.PKI) *cobra.Command {
	var yes, trustVMs bool
	report := func(rep *pki.RenewReport, err error) error {
		if err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "\nAncienne PKI archivée : %s\n", rep.Archive)
		if len(rep.Reissued) > 0 {
			fmt.Fprintf(os.Stderr, "Certificats réémis (nouvelles clés) : %s\n", strings.Join(rep.Reissued, ", "))
			fmt.Fprintln(os.Stderr, "À redéployer là où ils sont utilisés : labctl pki deploy VM NOM [DOSSIER]")
		}
		return nil
	}
	c := &cobra.Command{
		Use:   "renew intermediate|sub-ca NOM|root",
		Short: "Renouvelle la CA intermédiaire, une CA dérivée ou toute la chaîne (racine)",
		Long: `Renouvelle une partie de la chaîne avec de nouvelles clés et réémet à l'identique (mêmes noms, mêmes
usages, même durée) les certificats concernés. La PKI est d'abord archivée dans ` + filepath.Join(p.Dir, "archive") + `.

  intermediate   nouvelle intermédiaire, signée par la même racine : rien à faire sur les VMs
  sub-ca NOM     nouvelle CA dérivée, mêmes limites : rien à faire sur les VMs
  root           toute la chaîne recréée : les VMs doivent recevoir la nouvelle racine (--trust-vms ou
                 labctl pki trust-vm) et les certificats réémis doivent être redéployés`,
		Args: cobra.RangeArgs(1, 2),
		ValidArgsFunction: func(_ *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
			switch {
			case len(args) == 0:
				return []string{"intermediate", "sub-ca", "root"}, cobra.ShellCompDirectiveNoFileComp
			case len(args) == 1 && args[0] == "sub-ca":
				return p.SubCANames(), cobra.ShellCompDirectiveNoFileComp
			}
			return nil, cobra.ShellCompDirectiveNoFileComp
		},
		RunE: func(_ *cobra.Command, args []string) error {
			switch {
			case args[0] == "intermediate" && len(args) == 1:
				return report(p.RenewIntermediate())
			case args[0] == "sub-ca" && len(args) == 2:
				return report(p.RenewSubCA(args[1]))
			case args[0] == "root" && len(args) == 1:
				if !yes {
					if !confirmWord("Régénérer TOUTE la chaîne : nouvelle racine, toutes les CA et tous les certificats.\n"+
						"Les VMs ne feront plus confiance aux certificats tant qu'elles n'auront pas la nouvelle racine.", "RACINE") {
						return errors.New("abandon : rien n'a été modifié")
					}
				}
				if err := report(p.RotateRoot()); err != nil {
					return err
				}
				if trustVMs {
					var running []string
					for _, vm := range l.AllVMNames() {
						if lab.DomState(vm) == virt.StateRunning {
							running = append(running, vm)
						}
					}
					fmt.Fprintf(os.Stderr, "\n==> installation de la nouvelle racine sur les VMs démarrées : %s\n", strings.Join(running, " "))
					if err := p.TrustVM(l.Remote(), running...); err != nil {
						fmt.Fprintln(os.Stderr, "attention:", err, "(VMs sans accès SSH par clé : installer la racine à la main)")
					}
				} else {
					fmt.Fprintln(os.Stderr, "Installer la nouvelle racine sur les VMs : labctl pki trust-vm VM...")
				}
				return nil
			}
			return errors.New("usage : labctl pki renew intermediate | sub-ca NOM | root")
		},
	}
	c.Flags().BoolVarP(&yes, "yes", "y", false, "racine : ne pas demander de confirmation")
	c.Flags().BoolVar(&trustVMs, "trust-vms", false, "racine : installer ensuite la nouvelle racine sur les VMs démarrées")
	return c
}

// confirmWord demande de taper un mot exact pour confirmer (refus si l'entrée n'est pas un terminal).
func confirmWord(msg, word string) bool {
	if st, err := os.Stdin.Stat(); err != nil || st.Mode()&os.ModeCharDevice == 0 {
		fmt.Fprintln(os.Stderr, msg+"\nConfirmation impossible sans terminal : ajouter --yes.")
		return false
	}
	fmt.Fprintf(os.Stderr, "%s\nTaper %s pour confirmer : ", msg, word)
	var answer string
	fmt.Scanln(&answer)
	return answer == word
}

func without(list, exclude []string) []string {
	var out []string
	for _, x := range list {
		keep := true
		for _, e := range exclude {
			keep = keep && x != e
		}
		if keep {
			out = append(out, x)
		}
	}
	return out
}
