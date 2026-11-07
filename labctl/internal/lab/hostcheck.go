package lab

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"

	"labs/labctl/internal/virt"
)

// HostCheck vérifie que l'hôte peut faire tourner les labs : KVM, libvirt, outils, réseau NAT, clé SSH,
// accès de qemu au stockage. tools : commandes en plus à exiger. Renvoie le nombre de points à corriger.
func (l *Lab) HostCheck(out io.Writer, tools ...string) int {
	fail := 0
	chk := func(ok bool, what, fix string) {
		if ok {
			fmt.Fprintf(out, "ok  %s\n", what)
			return
		}
		fmt.Fprintf(out, "attention: %s -> %s\n", what, fix)
		fail++
	}
	cpu, _ := os.ReadFile("/proc/cpuinfo")
	chk(bytes.Contains(cpu, []byte(" vmx")) || bytes.Contains(cpu, []byte(" svm")), "virtualisation matérielle", "activer VT-x/AMD-V dans le BIOS")
	f, err := os.OpenFile("/dev/kvm", os.O_RDWR, 0)
	if err == nil {
		f.Close()
	}
	chk(err == nil, "/dev/kvm accessible", "sudo usermod -aG kvm $USER")
	nets, err := virt.Shared().Networks()
	chk(err == nil, "connexion à libvirt (qemu:///system)", "sudo usermod -aG libvirt $USER (puis reconnexion) ; sudo systemctl enable --now libvirtd.socket")
	if err == nil {
		def := l.DefaultNetwork()
		active := false
		for _, n := range nets {
			active = active || n.Name == def && n.Active
		}
		chk(active, "réseau libvirt "+def+" actif (sortie Internet de la passerelle)", "virsh -c qemu:///system net-start "+def)
	}
	for _, t := range append([]string{"qemu-img", "cloud-localds", "ssh", "gpgv"}, tools...) {
		_, err := exec.LookPath(t)
		chk(err == nil, "commande "+t, "sudo apt install qemu-utils cloud-image-utils openssh-client p7zip-full gpgv")
	}
	key := l.SSHPubKey()
	_, err = os.Stat(key)
	chk(err == nil, "clé SSH "+key, "ssh-keygen -t ed25519")

	// qemu (utilisateur libvirt-qemu) doit pouvoir traverser chaque dossier parent du stockage
	seen := map[string]bool{}
	for _, dir := range []string{l.VMDir(), l.ImagesDir()} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			chk(false, "dossier "+dir, err.Error())
			continue
		}
		for d := dir; d != "/" && !seen[d]; d = filepath.Dir(d) {
			seen[d] = true
			chk(qemuCanTraverse(d), "libvirt-qemu peut traverser "+d, "setfacl -m u:libvirt-qemu:x '"+d+"'")
		}
	}
	return fail
}

// qemuCanTraverse : droit x pour « autres », ou entrée ACL pour libvirt-qemu.
func qemuCanTraverse(dir string) bool {
	st, err := os.Stat(dir)
	if err != nil {
		return false
	}
	if st.Mode().Perm()&0o001 != 0 {
		return true
	}
	if u, err := user.Lookup("libvirt-qemu"); err == nil {
		if fmt.Sprint(statUID(st)) == u.Uid {
			return st.Mode().Perm()&0o100 != 0
		}
	}
	out, err := exec.Command("getfacl", "-p", dir).Output()
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "user:libvirt-qemu:") && strings.Contains(line[len("user:libvirt-qemu:"):], "x") {
			return true
		}
	}
	return false
}
