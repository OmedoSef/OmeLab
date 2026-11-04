package cli

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"
)

// Pare-feu de l'hôte côté réseaux des modules : les VMs des labs (cibles vulnérables comprises) ne doivent
// pas pouvoir ouvrir de connexion vers ce poste. L'hôte ne rend aucun service aux modules (DHCP et DNS
// sont sur la passerelle core-gw) : on refuse tout ce qui arrive des bridges des modules, sauf les
// réponses aux connexions ouvertes depuis l'hôte (SSH vers une VM, navigateur vers une cible...).
//
// Convention : les bridges des modules s'appellent virbr-<module> ; le réseau libvirt « default »
// (virbr0, NAT de la passerelle) n'est pas concerné.

const (
	hostfwTable = "labs_host"
	hostfwFile  = "/etc/labs/hostfw.nft"
	hostfwUnit  = "labs-hostfw.service"
	hostfwUnitP = "/etc/systemd/system/" + hostfwUnit
	nftBin      = "/usr/sbin/nft"
)

// hostfwRules : règles idempotentes (la table est recréée à chaque chargement).
const hostfwRules = `#!/usr/sbin/nft -f
# Labs : pare-feu de l'hôte vers les réseaux des modules (généré par labctl hostfw on)
# Les VMs des modules (bridges virbr-*) ne peuvent pas ouvrir de connexion vers l'hôte ;
# les réponses aux connexions ouvertes depuis l'hôte passent.
table inet ` + hostfwTable + `
delete table inet ` + hostfwTable + `
table inet ` + hostfwTable + ` {
	chain input {
		type filter hook input priority -10; policy accept;
		iifname "virbr-*" ct state established,related accept
		iifname "virbr-*" counter drop
	}
}
`

const hostfwService = `[Unit]
Description=Labs : pare-feu de l'hôte vers les réseaux des modules
Documentation=file://` + hostfwFile + `
DefaultDependencies=no
Before=network-pre.target libvirtd.service
Wants=network-pre.target
After=nftables.service

[Service]
Type=oneshot
RemainAfterExit=yes
ExecStart=` + nftBin + ` -f ` + hostfwFile + `
ExecStop=` + nftBin + ` delete table inet ` + hostfwTable + `

[Install]
WantedBy=multi-user.target
`

// requireRoot explique quoi faire quand la commande n'est pas lancée en root.
func requireRoot(args string) error {
	if os.Geteuid() == 0 {
		return nil
	}
	self, err := os.Executable()
	if err != nil {
		self = "labctl"
	}
	return fmt.Errorf("droits insuffisants : cette commande modifie le pare-feu de l'hôte et doit être lancée en root.\n"+
		"Relancer avec sudo (chemin complet : sudo ne garde pas le PATH de « source ./activate ») :\n\n  sudo %s %s\n", self, args)
}

func run(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	return cmd.Run()
}

func hostfwCmd() *cobra.Command {
	c := &cobra.Command{
		Use:     "hostfw",
		Short:   "Pare-feu de l'hôte : les VMs des modules ne peuvent pas se connecter à ce poste",
		GroupID: "outils",
		Long: `Pare-feu de l'hôte vers les réseaux des modules (bridges virbr-<module>).

Les VMs des labs, cibles vulnérables comprises, ne peuvent ouvrir aucune connexion vers ce poste ;
les réponses aux connexions que tu ouvres (SSH vers une VM, navigateur vers une cible) passent.
L'hôte ne rend aucun service aux modules (DHCP et DNS sont sur la passerelle core-gw).
Le réseau libvirt « default » (virbr0) n'est pas concerné. Les futurs modules sont couverts
automatiquement s'ils suivent la convention de nommage des bridges.

« on » et « off » demandent les droits root (sudo) ; « status » et « show » non.`,
	}
	c.AddCommand(&cobra.Command{
		Use:   "show",
		Short: "Affiche les règles nftables qui seraient chargées",
		Args:  cobra.NoArgs, ValidArgsFunction: cobra.NoFileCompletions,
		RunE: func(*cobra.Command, []string) error {
			fmt.Print(hostfwRules)
			return nil
		},
	})
	c.AddCommand(&cobra.Command{
		Use:   "status",
		Short: "Indique si le pare-feu est installé et actif",
		Args:  cobra.NoArgs, ValidArgsFunction: cobra.NoFileCompletions,
		RunE: func(*cobra.Command, []string) error {
			_, errFile := os.Stat(hostfwFile)
			enabled := exec.Command("systemctl", "is-enabled", "--quiet", hostfwUnit).Run() == nil
			active := exec.Command("systemctl", "is-active", "--quiet", hostfwUnit).Run() == nil
			yesno := func(b bool) string {
				if b {
					return "oui"
				}
				return "NON"
			}
			fmt.Printf("règles installées (%s) : %s\n", hostfwFile, yesno(errFile == nil))
			fmt.Printf("chargées au démarrage (%s) : %s\n", hostfwUnit, yesno(enabled))
			fmt.Printf("service actif : %s\n", yesno(active))
			if os.Geteuid() == 0 {
				out, err := exec.Command(nftBin, "list", "table", "inet", hostfwTable).CombinedOutput()
				if err != nil {
					fmt.Println("table nftables : absente")
				} else {
					fmt.Printf("table nftables :\n%s", out)
				}
			} else {
				fmt.Println("(détail de la table nftables : relancer en root)")
			}
			if errFile != nil || !active {
				fmt.Println("\nLes VMs des modules peuvent joindre ce poste : activer avec « sudo labctl hostfw on » (chemin complet de labctl).")
			}
			return nil
		},
	})
	c.AddCommand(&cobra.Command{
		Use:   "on",
		Short: "Active le pare-feu (immédiatement et à chaque démarrage) — sudo",
		Args:  cobra.NoArgs, ValidArgsFunction: cobra.NoFileCompletions,
		RunE: func(*cobra.Command, []string) error {
			if err := requireRoot("hostfw on"); err != nil {
				return err
			}
			if err := os.MkdirAll("/etc/labs", 0o755); err != nil {
				return err
			}
			// Contrôle de syntaxe avant d'installer quoi que ce soit
			tmp := hostfwFile + ".new"
			if err := os.WriteFile(tmp, []byte(hostfwRules), 0o644); err != nil {
				return err
			}
			if err := run(nftBin, "-c", "-f", tmp); err != nil {
				os.Remove(tmp)
				return fmt.Errorf("règles refusées par nft : %w", err)
			}
			if err := os.Rename(tmp, hostfwFile); err != nil {
				return err
			}
			if err := os.WriteFile(hostfwUnitP, []byte(hostfwService), 0o644); err != nil {
				return err
			}
			for _, args := range [][]string{{"daemon-reload"}, {"enable", "--quiet", hostfwUnit}, {"restart", hostfwUnit}} {
				if err := run("systemctl", args...); err != nil {
					return fmt.Errorf("systemctl %s : %w", strings.Join(args, " "), err)
				}
			}
			fmt.Println("ok  pare-feu de l'hôte actif : les VMs des modules (virbr-*) ne peuvent plus se connecter à ce poste")
			fmt.Println("    rechargé à chaque démarrage (" + hostfwUnit + ") ; « labctl hostfw off » pour le retirer")
			return nil
		},
	})
	c.AddCommand(&cobra.Command{
		Use:   "off",
		Short: "Retire le pare-feu (et son chargement au démarrage) — sudo",
		Args:  cobra.NoArgs, ValidArgsFunction: cobra.NoFileCompletions,
		RunE: func(*cobra.Command, []string) error {
			if err := requireRoot("hostfw off"); err != nil {
				return err
			}
			_ = exec.Command("systemctl", "disable", "--now", "--quiet", hostfwUnit).Run()
			_ = exec.Command(nftBin, "delete", "table", "inet", hostfwTable).Run()
			var errs []error
			for _, f := range []string{hostfwUnitP, hostfwFile} {
				if err := os.Remove(f); err != nil && !os.IsNotExist(err) {
					errs = append(errs, err)
				}
			}
			_ = exec.Command("systemctl", "daemon-reload").Run()
			if err := errors.Join(errs...); err != nil {
				return err
			}
			fmt.Println("ok  pare-feu de l'hôte retiré : les VMs des modules peuvent de nouveau joindre ce poste")
			return nil
		},
	})
	return c
}
