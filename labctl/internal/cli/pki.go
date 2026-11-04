package cli

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"labs/labctl/internal/lab"
	"labs/labctl/internal/pki"
)

// sshOpts reprend les options SSH des scripts : les IP des labs sont réutilisées, on n'alimente pas known_hosts.
var sshOpts = []string{"-o", "StrictHostKeyChecking=no", "-o", "UserKnownHostsFile=/dev/null", "-o", "LogLevel=ERROR", "-o", "ConnectTimeout=5"}

func newPKI(l *lab.Lab) *pki.PKI {
	return &pki.PKI{
		Dir:    l.PKIDir(),
		Org:    l.Setting("LABS_PKI_ORG", "Omedo Labs"),
		Domain: l.Setting("LABS_PKI_DOMAIN", "omedo.lab"),
		Out:    os.Stderr,
	}
}

// remote résout l'IP d'une VM avec scripts/vm-ip.sh (couche VM pas encore portée en Go).
func remote(l *lab.Lab) pki.Remote {
	return pki.Remote{
		User:    l.Setting("LABS_VM_USER", "lab"),
		SSHOpts: sshOpts,
		IP: func(vm string) (string, error) {
			out, err := exec.Command(filepath.Join(l.Root, "scripts", "vm-ip.sh"), vm).Output()
			if err != nil {
				return "", errors.New("pas d'IP (VM démarrée ?)")
			}
			return strings.TrimSpace(string(out)), nil
		},
	}
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
		Example: "  labctl pki sub-ca pentest -D pentest-lab.omedo.lab -s 10.66.66.0/24",
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
		Example: `  labctl pki issue web -C pentest -i 10.66.66.30 -d dvwa.pentest-lab.omedo.lab
  labctl pki issue ingress -D k8s.omedo.lab -d '*.apps.k8s.omedo.lab'
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
	root.AddCommand(&cobra.Command{
		Use:   "trust-vm VM...",
		Short: "Installe la CA racine dans des VMs existantes (SSH)",
		Args:  cobra.MinimumNArgs(1),
		ValidArgsFunction: func(_ *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
			return without(l.AllVMNames(), args), cobra.ShellCompDirectiveNoFileComp
		},
		RunE: func(_ *cobra.Command, args []string) error { return p.TrustVM(remote(l), args...) },
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
			return p.Deploy(remote(l), args[0], args[1], dest)
		},
	})
	root.AddCommand(&cobra.Command{
		Use:   "trust-host",
		Short: "Installe la CA racine sur cet hôte (sudo)",
		Args:  cobra.NoArgs, ValidArgsFunction: noFiles,
		RunE: func(*cobra.Command, []string) error { return p.TrustHost() },
	})
	return root
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
