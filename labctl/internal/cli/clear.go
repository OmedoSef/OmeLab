package cli

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"labs/labctl/internal/lab"
)

// clearCmd : « labctl clear », remise à zéro du lab. Réservée au terminal : la console de la GUI ne la propose pas
// (elle répond oui d'office aux confirmations).
func clearCmd(l *lab.Lab) *cobra.Command {
	var o lab.ClearOptions
	var yes bool
	c := &cobra.Command{
		Use:     "clear [--pki] [--images]",
		Short:   "Vide le lab : VMs et réseaux de tous les modules, passerelle (--pki, --images : aussi la PKI, les téléchargements)",
		GroupID: "outils",
		Long: `Vide le lab : chaque module est détruit (VMs, disques, réseau, comme « labctl <module> destroy »), puis la
passerelle core-gw. Une seule confirmation, après le détail de ce qui sera supprimé.

  --pki     supprime aussi la PKI (CA racine, intermédiaire, CA des modules, certificats). Les VMs recréées feront
            confiance à la nouvelle CA (labctl pki init) ; si « labctl pki trust-host » avait été fait, retirer
            l'ancienne racine des autorités de ce poste.
  --images  supprime aussi ce que labctl a téléchargé : images du catalogue, disques éditeurs convertis, cache
            (archives, mitmproxy, pilotes Windows). Ils seront retéléchargés au prochain « up ».

Restent toujours : les module.yaml et files/ des modules, config.env, lab.yaml et les fichiers *.local.*, les VMs
créées à la main hors des modules (labctl vm, labctl lab) et les fichiers déposés à la main dans le dossier des
images (ISO Windows…). Pour un module seul : labctl <module> destroy.`,
		Args:              cobra.NoArgs,
		ValidArgsFunction: cobra.NoFileCompletions,
		RunE: func(cmd *cobra.Command, _ []string) error {
			p, err := l.PlanClear(o)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if p.Empty() {
				fmt.Fprintln(out, "ok  rien à supprimer : le lab est déjà vide")
				return nil
			}
			fmt.Fprintln(out, "Seront supprimés :")
			if len(p.Modules) > 0 {
				vms := "aucune VM"
				if len(p.VMs) > 0 {
					vms = strings.Join(p.VMs, ", ")
				}
				fmt.Fprintf(out, "  modules    : %s (réseaux, et VMs : %s)\n", strings.Join(p.Modules, ", "), vms)
			}
			if p.Gateway {
				fmt.Fprintf(out, "  passerelle : %s\n", lab.GatewayVM)
			}
			if p.PKIDir != "" {
				fmt.Fprintf(out, "  PKI        : %s (clés et certificats)\n", p.PKIDir)
			} else if o.PKI {
				fmt.Fprintln(out, "  PKI        : aucune")
			}
			if o.Images {
				fmt.Fprintf(out, "  téléchargements : %d élément(s), %d Mio\n", len(p.Files), p.Bytes>>20)
				for _, f := range p.Files {
					fmt.Fprintf(out, "    %s\n", f)
				}
				for _, k := range p.Kept {
					fmt.Fprintf(out, "  gardé : %s\n", k)
				}
			}
			fmt.Fprintln(out, "Restent : module.yaml et files/, configuration du lab, VMs créées à la main, fichiers déposés à la main.")
			if !yes && !confirmYN("Tout supprimer ? (irréversible)") {
				return errors.New("abandon")
			}
			return l.Clear(p, os.Stdout)
		},
	}
	c.Flags().BoolVar(&o.PKI, "pki", false, "supprime aussi la PKI (CA et certificats)")
	c.Flags().BoolVar(&o.Images, "images", false, "supprime aussi les images et sources téléchargées par labctl")
	c.Flags().BoolVarP(&yes, "yes", "y", false, "sans confirmation")
	return c
}
