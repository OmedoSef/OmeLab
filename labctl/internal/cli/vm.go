package cli

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"labs/labctl/internal/lab"
	"labs/labctl/internal/virt"
)

func vmNames(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
	doms, _ := virt.Shared().Domains()
	out := make([]string, len(doms))
	for i, d := range doms {
		out[i] = d.Name
	}
	return out, cobra.ShellCompDirectiveNoFileComp
}

func vmCmd(l *lab.Lab) *cobra.Command {
	c := &cobra.Command{Use: "vm", Short: "VMs : liste, adresse IP, snapshots", GroupID: "outils"}

	var labFilter string
	var managed bool
	list := &cobra.Command{
		Use:   "list",
		Short: "Liste les VMs avec leur état, leur lab et leur IP",
		Args:  cobra.NoArgs, ValidArgsFunction: cobra.NoFileCompletions,
		RunE: func(*cobra.Command, []string) error {
			doms, err := virt.Shared().Domains()
			if err != nil {
				return err
			}
			// Les IP se cherchent en parallèle (agent invité, passerelle...)
			ips := make([]string, len(doms))
			var wg sync.WaitGroup
			for i, d := range doms {
				if d.State != "running" {
					ips[i] = "-"
					continue
				}
				wg.Add(1)
				go func() {
					defer wg.Done()
					if ips[i] = l.VMIP(d.Name); ips[i] == "" {
						ips[i] = "?"
					}
				}()
			}
			wg.Wait()
			tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "NOM\tETAT\tLAB\tIP")
			for i, d := range doms {
				vl := lab.VMLab(d.Description)
				if labFilter != "" && vl != labFilter || managed && !strings.Contains(d.Description, lab.LabMarker) {
					continue
				}
				if vl == "" {
					vl = "-"
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", d.Name, d.State, vl, ips[i])
			}
			return tw.Flush()
		},
	}
	list.Flags().StringVarP(&labFilter, "lab", "l", "", "seulement les VMs de ce lab")
	list.Flags().BoolVarP(&managed, "managed", "m", false, "seulement les VMs créées par les scripts")
	c.AddCommand(list)

	var wait int
	ip := &cobra.Command{
		Use:   "ip VM",
		Short: "Affiche l'IP d'une VM (bail DHCP, agent invité, ARP, baux de la passerelle)",
		Args:  cobra.ExactArgs(1), ValidArgsFunction: vmNames,
		RunE: func(_ *cobra.Command, args []string) error {
			if virt.Shared().State(args[0]) == "absente" {
				return fmt.Errorf("VM inconnue : %s", args[0])
			}
			addr := ""
			if wait > 0 {
				addr = l.WaitVMIP(args[0], wait)
			} else {
				addr = l.VMIP(args[0])
			}
			if addr == "" {
				return fmt.Errorf("pas d'IP pour %s (VM démarrée ? qemu-guest-agent installé ?)", args[0])
			}
			fmt.Println(addr)
			return nil
		},
	}
	ip.Flags().IntVarP(&wait, "wait", "w", 0, "attendre jusqu'à N secondes qu'elle en ait une")
	c.AddCommand(ip)

	c.AddCommand(snapshotCmd())
	return c
}

func snapshotCmd() *cobra.Command {
	v := virt.Shared()
	snapNames := func(_ *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
		if len(args) == 0 {
			return vmNames(nil, nil, "")
		}
		if len(args) == 1 {
			return v.SnapshotNames(args[0]), cobra.ShellCompDirectiveNoFileComp
		}
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	c := &cobra.Command{Use: "snapshot", Short: "Snapshots internes qcow2 (VM allumée ou éteinte)"}
	c.AddCommand(&cobra.Command{
		Use:   "create VM NOM [description]",
		Short: "Prend un snapshot",
		Args:  cobra.RangeArgs(2, 3), ValidArgsFunction: vmNames,
		RunE: func(_ *cobra.Command, args []string) error {
			desc := ""
			if len(args) == 3 {
				desc = args[2]
			}
			if err := v.CreateSnapshot(args[0], args[1], desc); err != nil {
				return err
			}
			fmt.Fprintf(os.Stderr, "ok  snapshot %s créé sur %s\n", args[1], args[0])
			return nil
		},
	})
	c.AddCommand(&cobra.Command{
		Use:   "list VM",
		Short: "Liste les snapshots d'une VM",
		Args:  cobra.ExactArgs(1), ValidArgsFunction: vmNames,
		RunE: func(_ *cobra.Command, args []string) error {
			snaps, err := v.Snapshots(args[0])
			if err != nil {
				return err
			}
			tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "NOM\tCRÉÉ LE\tÉTAT DE LA VM\tDESCRIPTION")
			for _, s := range snaps {
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", s.Name, s.Created.Format(time.DateTime), s.State, s.Desc)
			}
			return tw.Flush()
		},
	})
	c.AddCommand(&cobra.Command{
		Use:   "revert VM NOM",
		Short: "Restaure une VM à un snapshot",
		Args:  cobra.ExactArgs(2), ValidArgsFunction: snapNames,
		RunE: func(_ *cobra.Command, args []string) error {
			if err := v.RevertSnapshot(args[0], args[1]); err != nil {
				return err
			}
			fmt.Fprintf(os.Stderr, "ok  %s restaurée à %s\n", args[0], args[1])
			return nil
		},
	})
	c.AddCommand(&cobra.Command{
		Use:   "delete VM NOM",
		Short: "Supprime un snapshot",
		Args:  cobra.ExactArgs(2), ValidArgsFunction: snapNames,
		RunE: func(_ *cobra.Command, args []string) error {
			if err := v.DeleteSnapshot(args[0], args[1]); err != nil {
				return err
			}
			fmt.Fprintf(os.Stderr, "ok  snapshot %s supprimé de %s\n", args[1], args[0])
			return nil
		},
	})
	return c
}

var errAbort = errors.New("abandon")
