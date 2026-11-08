package lab

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"labs/labctl/internal/fetch"
	"labs/labctl/internal/virt"
	"labs/labctl/internal/vmdef"
)

// DefaultVMSpec renvoie une description avec les valeurs par défaut des réglages (config.env).
func (l *Lab) DefaultVMSpec(name string) vmdef.Spec {
	atoi := func(k string, def int) int {
		if n, err := strconv.Atoi(l.Setting(k, "")); err == nil {
			return n
		}
		return def
	}
	return vmdef.Spec{
		Name:      name,
		Image:     l.DefaultImage(),
		CPUs:      atoi("LABS_DEFAULT_CPUS", 1),
		MemoryMiB: atoi("LABS_DEFAULT_MEMORY", 2048),
		DiskGiB:   atoi("LABS_DEFAULT_DISK", 20),
	}
}

// VMDir renvoie le dossier des VMs créées par labctl (une VM = un sous-dossier).
func (l *Lab) VMDir() string {
	return l.Setting("LABS_VM_DIR", filepath.Join(l.Setting("LABS_STORAGE_DIR", ""), "vms"))
}

// Keyboard renvoie la disposition de clavier des VMs : LABS_KEYBOARD, sinon celle de ce poste, sinon us.
func (l *Lab) Keyboard() string {
	if kb := l.Setting("LABS_KEYBOARD", ""); kb != "" {
		return kb
	}
	if out, err := exec.Command("localectl", "status").Output(); err == nil {
		for _, line := range strings.Split(string(out), "\n") {
			if k, v, ok := strings.Cut(strings.TrimSpace(line), ":"); ok && k == "X11 Layout" {
				if kb, _, _ := strings.Cut(strings.TrimSpace(v), ","); kb != "" {
					return kb
				}
			}
		}
	}
	return "us"
}

// NetDomain renvoie le domaine DNS des VMs d'un réseau : celui du module qui l'utilise, sinon celui déclaré
// dans libvirt, sinon le domaine racine des labs.
func (l *Lab) NetDomain(network string) string {
	if mods, err := l.Modules(); err == nil {
		for _, m := range mods {
			if m.Env != nil && m.Env.Net == network {
				return m.Env.Domain
			}
		}
	}
	if nets, err := l.Hypervisor().Networks(); err == nil {
		for _, n := range nets {
			if n.Name == network && n.Domain != "" {
				return n.Domain
			}
		}
	}
	return l.pkiDomain()
}

// CreateOptions règle CreateVM.
type CreateOptions struct {
	DryRun bool      // affiche ce qui serait créé, sans rien créer
	Wait   bool      // attend l'IP et la fin de cloud-init
	Out    io.Writer // messages
}

// CreateVM crée et démarre une VM depuis une image cloud : disque qcow2 lié à l'image de base, disque
// cloud-init, définition libvirt. En cas d'échec, rien n'est laissé derrière.
func (l *Lab) CreateVM(s vmdef.Spec, o CreateOptions) (err error) {
	out := o.Out
	if out == nil {
		out = io.Discard
	}
	v := l.Hypervisor()
	if err := s.Validate(); err != nil {
		return err
	}
	if !o.DryRun && v.State(s.Name) != virt.StateAbsent {
		return fmt.Errorf("la VM %s existe déjà", s.Name)
	}

	// Environnement : réseaux, domaine, passerelle, clés, CA, fichiers
	nets := map[string]virt.Network{}
	if all, err := v.Networks(); err == nil {
		for _, n := range all {
			nets[n.Name] = n
		}
	}
	for i := range s.Nets {
		if _, ok := nets[s.Nets[i].Network]; !ok && !o.DryRun {
			return fmt.Errorf("réseau libvirt inexistant : %s (labctl net list)", s.Nets[i].Network)
		}
		if s.Nets[i].MAC == "" {
			s.Nets[i].MAC = vmdef.RandomMAC()
		}
	}
	keyboard := s.Keyboard // override par VM (module.yaml) ; vide : disposition de l'hôte
	if keyboard == "" {
		keyboard = l.Keyboard()
	}
	env := vmdef.CloudEnv{
		Domain:    l.NetDomain(s.Nets[0].Network),
		Gateway:   s.Gateway,
		Keyboard:  keyboard,
		User:      l.VMUser(),
		Password:  l.Setting("LABS_VM_PASSWORD", "lab"),
		Files:     map[string][]byte{},
		InstantID: vmdef.InstanceID(s.Name),
	}
	if first := s.Nets[0]; first.Address != "" && env.Gateway == "" {
		if n := nets[first.Network]; n.Forward == "nat" || n.Forward == "route" {
			env.Gateway = n.HostIP
		}
	}
	cloud := s.Base == "" && s.Install == nil // image cloud : cloud-init ; sinon disque fourni ou installation depuis ISO
	keyPath := l.SSHPubKey()
	key, err := os.ReadFile(keyPath)
	if err != nil && cloud {
		return fmt.Errorf("clé SSH publique introuvable : %s", keyPath)
	}
	env.PubKey = string(key)
	if ca, err := os.ReadFile(filepath.Join(l.PKIDir(), "root", "root-ca.crt")); err == nil {
		env.RootCA = string(ca)
	}
	for _, f := range s.Files {
		b, err := os.ReadFile(f.Src)
		if err != nil {
			return fmt.Errorf("fichier introuvable : %s", f.Src)
		}
		env.Files[f.Src] = b
	}
	if s.UserData != "" {
		if env.UserData, err = os.ReadFile(s.UserData); err != nil {
			return fmt.Errorf("fichier introuvable : %s", s.UserData)
		}
	}
	ci := vmdef.RenderCloudInit(&s, env)

	dir := filepath.Join(l.VMDir(), s.Name)
	disk, seed := filepath.Join(dir, "disk.qcow2"), filepath.Join(dir, "seed.iso")
	desc := LabMarker
	if s.Lab != "" {
		desc += " lab=" + s.Lab
	}
	if s.Role != "" {
		desc += " role=" + s.Role
	}
	if !cloud {
		seed = ""
	}
	domXML := vmdef.RenderDomainXML(&s, disk, seed, desc)

	if o.DryRun && !cloud {
		fmt.Fprintf(out, "----- disque fourni\n%s\n----- domaine libvirt\n%s", s.Base, domXML)
		return nil
	}
	if o.DryRun {
		fmt.Fprintf(out, "----- user-data\n%s----- network-config\n%s----- meta-data\n%s----- domaine libvirt\n%s",
			ci.UserData, ci.NetworkConfig, ci.MetaData, domXML)
		return nil
	}

	base := s.Base
	if cloud {
		if base, err = l.EnsureImage(s.Image, false, out); err != nil {
			return err
		}
	}
	if _, err := os.Stat(dir); err == nil {
		return fmt.Errorf("le dossier %s existe déjà (reste d'une VM supprimée à la main ?)", dir)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	defined := false
	defer func() {
		if err != nil { // rien ne doit rester d'une création ratée
			if defined {
				if v.State(s.Name) == virt.StateRunning {
					_ = v.DestroyDomain(s.Name) // l'échec se voit à la suppression qui suit
				}
				if uerr := v.UndefineDomain(s.Name); uerr != nil {
					fmt.Fprintf(out, "attention: %s n'a pas pu être retirée de libvirt (labctl vm destroy %s) : %v\n", s.Name, s.Name, uerr)
				}
			}
			if rerr := os.RemoveAll(dir); rerr != nil {
				fmt.Fprintf(out, "attention: dossier %s non supprimé : %v\n", dir, rerr)
			}
		}
	}()

	what := fmt.Sprintf("%s, %d vCPU, %d Mio, %d Gio", s.Image, s.CPUs, s.MemoryMiB, s.DiskGiB)
	switch {
	case s.Install != nil:
		what = fmt.Sprintf("installation depuis %s, %d vCPU, %d Mio, %d Gio", filepath.Base(s.Install.ISOs[0]), s.CPUs, s.MemoryMiB, s.DiskGiB)
	case !cloud:
		what = fmt.Sprintf("disque fourni, %d vCPU, %d Mio", s.CPUs, s.MemoryMiB)
	}
	fmt.Fprintf(out, "==> Création de %s (%s)\n", s.Name, what)
	// Disque lié à l'image de base (un disque fourni garde sa taille d'origine) ; installation : disque vierge
	if s.Install == nil { // image de base : contrôlée à chaque création (y compris si téléchargée avant ce contrôle)
		if f, err := fetch.CheckDiskUsing(l.tool, base); err != nil {
			return err
		} else if f != "qcow2" {
			return fmt.Errorf("image de base %s : qcow2 attendu, %s trouvé", filepath.Base(base), f)
		}
	}
	args := []string{"create", "-q", "-f", "qcow2", "-F", "qcow2", "-b", base, disk}
	switch {
	case s.Install != nil:
		args = []string{"create", "-q", "-f", "qcow2", disk, fmt.Sprintf("%dG", s.DiskGiB)}
	case cloud:
		args = append(args, fmt.Sprintf("%dG", s.DiskGiB))
	}
	if o, e, err := l.tool("qemu-img", args...); err != nil {
		return fmt.Errorf("qemu-img : %s", strings.TrimSpace(string(e)+string(o)))
	}
	if cloud {
		for name, content := range map[string]string{"user-data": ci.UserData, "network-config": ci.NetworkConfig, "meta-data": ci.MetaData} {
			if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
				return err
			}
		}
		if o, e, err := l.tool("cloud-localds", "-N", filepath.Join(dir, "network-config"), seed,
			filepath.Join(dir, "user-data"), filepath.Join(dir, "meta-data")); err != nil {
			return fmt.Errorf("cloud-localds : %s", strings.TrimSpace(string(e)+string(o)))
		}
	}
	if err := v.DefineDomain(domXML); err != nil {
		return fmt.Errorf("définition refusée par libvirt : %w", err)
	}
	defined = true
	if err := v.StartDomain(s.Name); err != nil {
		return fmt.Errorf("démarrage de %s : %w", s.Name, err)
	}
	lab := ""
	if s.Lab != "" {
		lab = " (lab " + s.Lab + ")"
	}
	fmt.Fprintf(out, "ok  VM %s.%s démarrée%s\n", s.Name, env.Domain, lab)

	if o.Wait && cloud {
		return l.waitReady(s.Name, env.User, out)
	}
	return nil
}

// waitReady attend l'IP, l'accès SSH, puis la fin de cloud-init.
func (l *Lab) waitReady(name, user string, out io.Writer) error {
	fmt.Fprintln(out, "==> Attente de l'adresse IP...")
	ip := l.WaitVMIP(name, 300)
	if ip == "" {
		return fmt.Errorf("pas d'IP au bout de 5 min (console : virsh console %s)", name)
	}
	fmt.Fprintf(out, "==> IP %s, attente de cloud-init...\n", ip)
	target := user + "@" + ip
	for i := 0; i < 60; i++ {
		if _, _, err := l.shell(target, "true", nil); err == nil {
			break
		}
		time.Sleep(3 * time.Second)
	}
	stdout, stderr, err := l.shell(target, "cloud-init status --wait >/dev/null; cloud-init status", nil)
	fmt.Fprint(out, string(stdout)+string(stderr))
	if err != nil {
		fmt.Fprintln(out, "attention: cloud-init a signalé une erreur (journal : sudo cat /var/log/cloud-init-output.log)")
	}
	fmt.Fprintf(out, "ok  %s prête : ssh %s  (ou labctl vm ssh %s)\n", name, target, name)
	return nil
}
