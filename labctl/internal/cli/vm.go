package cli

import (
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
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

	c.AddCommand(snapshotCmd(), vmCreateCmd(l), vmDestroyCmd(l), vmSSHCmd(l), vmKeyboardCmd(l))
	return c
}

func vmCreateCmd(l *lab.Lab) *cobra.Command {
	var image, gw, dns, labName, packages, userData string
	var cpus, mem, disk int
	var nets, files, runs []string
	var noCA, wait, dry bool
	c := &cobra.Command{
		Use:   "create NOM [options]",
		Short: "Crée une VM depuis une image cloud (disque lié à l'image de base + cloud-init)",
		Long: `Crée et démarre une VM depuis une image cloud du catalogue (labctl image list).

Réseau : -n RESEAU[:IP/CIDR], répétable ; la 1re carte est la carte de gestion. Sans IP : DHCP.
La VM reçoit l'utilisateur ` + l.Setting("LABS_VM_USER", "lab") + ` (sudo, ta clé SSH), le clavier de ce poste, la CA racine des labs
et qemu-guest-agent. Elle est rattachée à un lab avec -l.`,
		Example: `  labctl vm create web1 -l demo -w
  labctl vm create db1 -i rocky9 -m 4096 -n default -n lab-demo:10.10.0.11/24 -l demo`,
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: cobra.NoFileCompletions,
		RunE: func(cmd *cobra.Command, args []string) error {
			s := l.DefaultVMSpec(args[0])
			if cmd.Flags().Changed("image") {
				s.Image = image
			}
			if cmd.Flags().Changed("cpus") {
				s.CPUs = cpus
			}
			if cmd.Flags().Changed("memory") {
				s.MemoryMiB = mem
			}
			if cmd.Flags().Changed("disk") {
				s.DiskGiB = disk
			}
			if len(nets) == 0 {
				nets = []string{l.Setting("LABS_DEFAULT_NETWORK", "default")}
			}
			for _, n := range nets {
				s.Nets = append(s.Nets, lab.ParseNet(n))
			}
			s.Gateway, s.Lab, s.UserData, s.NoCA, s.Run = gw, labName, userData, noCA, runs
			if dns != "" {
				s.DNS = strings.Split(dns, ",")
			}
			if packages != "" {
				s.Packages = strings.Split(packages, ",")
			}
			for _, f := range files {
				fs, err := lab.ParseFile(f)
				if err != nil {
					return err
				}
				s.Files = append(s.Files, fs)
			}
			return l.CreateVM(s, lab.CreateOptions{DryRun: dry, Wait: wait, Out: os.Stderr})
		},
	}
	f := c.Flags()
	f.StringVarP(&image, "image", "i", "", "image du catalogue (défaut : "+l.Setting("LABS_DEFAULT_IMAGE", "debian13")+")")
	f.IntVarP(&cpus, "cpus", "c", 0, "vCPUs")
	f.IntVarP(&mem, "memory", "m", 0, "RAM en Mio")
	f.IntVarP(&disk, "disk", "d", 0, "disque en Gio")
	f.StringArrayVarP(&nets, "net", "n", nil, "carte réseau RESEAU[:IP/CIDR] (répétable)")
	f.StringVar(&gw, "gw", "", "passerelle de la 1re carte si IP fixe")
	f.StringVar(&dns, "dns", "", "serveurs DNS si IP fixe (IP,IP)")
	f.StringVarP(&labName, "lab", "l", "", "rattache la VM à un lab")
	f.StringVarP(&packages, "packages", "p", "", "paquets supplémentaires (a,b)")
	f.StringArrayVarP(&files, "file", "f", nil, "fichier local copié dans la VM SRC:DEST[:MODE] (répétable)")
	f.StringArrayVarP(&runs, "run", "r", nil, "commande lancée en fin d'installation (répétable)")
	f.StringVar(&userData, "user-data", "", "cloud-config remplaçant celui généré")
	f.BoolVar(&noCA, "no-ca", false, "ne pas injecter la CA racine des labs")
	f.BoolVarP(&wait, "wait", "w", false, "attendre la fin de cloud-init")
	f.BoolVar(&dry, "dry-run", false, "afficher cloud-init et la définition libvirt sans rien créer")
	_ = c.RegisterFlagCompletionFunc("image", cobra.FixedCompletions(lab.ImageNames(), cobra.ShellCompDirectiveNoFileComp))
	_ = c.RegisterFlagCompletionFunc("net", netNames)
	return c
}

func vmDestroyCmd(l *lab.Lab) *cobra.Command {
	var yes, force bool
	c := &cobra.Command{
		Use:   "destroy [-y] [--force] VM...",
		Short: "Supprime des VMs (définition, snapshots, dossier, baux DHCP)",
		Long: `Arrête et supprime des VMs : définition libvirt et snapshots, dossier dans le dossier des VMs, baux DHCP
sur la passerelle. Une VM non créée par les scripts est refusée sauf --force (ses disques hors du dossier
des VMs sont alors conservés). ASSUME_YES=1 équivaut à -y.`,
		Args:              cobra.MinimumNArgs(1),
		ValidArgsFunction: vmNames,
		RunE: func(_ *cobra.Command, args []string) error {
			yes = yes || os.Getenv("ASSUME_YES") == "1"
			var failed []string
			for _, name := range args {
				if !yes && !confirmYN(fmt.Sprintf("Supprimer définitivement la VM %s et ses disques ?", name)) {
					continue
				}
				if err := l.DestroyVM(name, force, os.Stderr); err != nil {
					fmt.Fprintln(os.Stderr, "attention:", err)
					failed = append(failed, name)
				}
			}
			if len(failed) > 0 {
				return fmt.Errorf("non supprimée(s) : %s", strings.Join(failed, " "))
			}
			return nil
		},
	}
	c.Flags().BoolVarP(&yes, "yes", "y", false, "pas de confirmation")
	c.Flags().BoolVar(&force, "force", false, "autoriser une VM non créée par les scripts")
	return c
}

func vmSSHCmd(l *lab.Lab) *cobra.Command {
	var user string
	c := &cobra.Command{
		Use:                   "ssh [-u USER] VM [commande...]",
		Short:                 "SSH vers une VM par son nom (sans alimenter known_hosts)",
		Args:                  cobra.MinimumNArgs(1),
		ValidArgsFunction:     vmNames,
		DisableFlagsInUseLine: true,
		RunE: func(_ *cobra.Command, args []string) error {
			ip := l.WaitVMIP(args[0], 60)
			if ip == "" {
				return fmt.Errorf("pas d'IP pour %s", args[0])
			}
			if user == "" {
				user = l.Setting("LABS_VM_USER", "lab")
			}
			// Juste après un démarrage ou une restauration, sshd peut mettre quelques secondes à répondre
			for i := 0; i < 20; i++ {
				if c, err := net.DialTimeout("tcp", ip+":22", 2*time.Second); err == nil {
					c.Close()
					break
				}
				time.Sleep(2 * time.Second)
			}
			sshPath, err := exec.LookPath("ssh")
			if err != nil {
				return err
			}
			argv := append(append([]string{"ssh"}, lab.SSHOpts...), user+"@"+ip)
			return syscall.Exec(sshPath, append(argv, args[1:]...), os.Environ())
		},
	}
	c.Flags().StringVarP(&user, "user", "u", "", "utilisateur (défaut : "+l.Setting("LABS_VM_USER", "lab")+")")
	c.Flags().SetInterspersed(false) // les options après VM appartiennent à la commande distante
	return c
}

func imageCmd(l *lab.Lab) *cobra.Command {
	c := &cobra.Command{Use: "image", Short: "Images cloud de base : catalogue et téléchargement vérifié", GroupID: "outils"}
	c.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "Catalogue, avec les images déjà téléchargées",
		Args:  cobra.NoArgs, ValidArgsFunction: cobra.NoFileCompletions,
		RunE: func(*cobra.Command, []string) error {
			tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "IMAGE\tLOCALE\tSOURCE")
			for _, n := range lab.ImageNames() {
				state := "-"
				if st, err := os.Stat(l.ImagePath(n)); err == nil {
					state = fmt.Sprintf("présente (%d Mo)", st.Size()>>20)
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\n", n, state, lab.Images[n].URL)
			}
			return tw.Flush()
		},
	})
	var force bool
	get := &cobra.Command{
		Use:   "get [-f] IMAGE...",
		Short: "Télécharge des images (empreinte vérifiée) ; -f : nouvelle version",
		Args:  cobra.MinimumNArgs(1),
		ValidArgsFunction: func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
			return lab.ImageNames(), cobra.ShellCompDirectiveNoFileComp
		},
		RunE: func(_ *cobra.Command, args []string) error {
			for _, n := range args {
				_, statErr := os.Stat(l.ImagePath(n))
				p, err := l.EnsureImage(n, force, os.Stderr)
				if err != nil {
					return err
				}
				if statErr == nil && !force {
					fmt.Fprintf(os.Stderr, "ok  %s déjà présente : %s\n", n, p)
				}
			}
			return nil
		},
	}
	get.Flags().BoolVarP(&force, "force", "f", false, "retélécharger (nouvelle version)")
	c.AddCommand(get)
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
