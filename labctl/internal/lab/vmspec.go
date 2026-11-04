package lab

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/xml"
	"fmt"
	"net/netip"
	"regexp"
	"strings"
	"time"
)

// VMSpec décrit une VM à créer depuis une image cloud. C'est la forme générique qu'utilisent
// « labctl vm create » et, demain, les entrées vms: des module.yaml.
type VMSpec struct {
	Name      string
	Image     string // image du catalogue (Images)
	CPUs      int
	MemoryMiB int
	DiskGiB   int
	Nets      []NetSpec // la 1re carte est la carte de gestion
	Gateway   string    // passerelle si la 1re carte a une IP fixe (défaut : hôte du réseau s'il est NAT/routé)
	DNS       []string  // DNS si IP fixe (défaut : la passerelle)
	Lab       string    // rattachement (description libvirt « lab=... »)
	Packages  []string  // paquets en plus de qemu-guest-agent
	Files     []FileSpec
	Run       []string // commandes shell lancées en fin d'installation, dans l'ordre
	UserData  string   // fichier cloud-config remplaçant celui généré
	NoCA      bool     // ne pas injecter la CA racine des labs
}

// NetSpec est une carte réseau.
type NetSpec struct {
	Network string // réseau libvirt
	Address string // IP fixe « 10.10.0.11/24 » ; vide = DHCP
	MAC     string // vide = générée
}

// FileSpec est un fichier local copié dans la VM.
type FileSpec struct {
	Src  string
	Dest string
	Mode string // défaut 0644
}

var reVMName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9-]*$`)

// ParseNet lit « RESEAU[:IP/CIDR] » (syntaxe de -n).
func ParseNet(s string) NetSpec {
	n, addr, _ := strings.Cut(s, ":")
	return NetSpec{Network: n, Address: addr}
}

// ParseFile lit « SRC:DEST[:MODE] » (syntaxe de -f).
func ParseFile(s string) (FileSpec, error) {
	p := strings.SplitN(s, ":", 3)
	if len(p) < 2 || p[0] == "" || p[1] == "" {
		return FileSpec{}, fmt.Errorf("fichier attendu sous la forme SRC:DEST[:MODE] : %q", s)
	}
	f := FileSpec{Src: p[0], Dest: p[1]}
	if len(p) == 3 {
		f.Mode = p[2]
	}
	return f, nil
}

// Validate contrôle la description (sans accès système).
func (s *VMSpec) Validate() error {
	switch {
	case !reVMName.MatchString(s.Name):
		return fmt.Errorf("nom invalide (lettres, chiffres, tirets) : %q", s.Name)
	case Images[s.Image].URL == "":
		return fmt.Errorf("image inconnue : %q (%s)", s.Image, strings.Join(ImageNames(), ", "))
	case s.CPUs < 1 || s.MemoryMiB < 256 || s.DiskGiB < 1:
		return fmt.Errorf("ressources invalides : %d vCPU, %d Mio, %d Gio", s.CPUs, s.MemoryMiB, s.DiskGiB)
	case len(s.Nets) == 0:
		return fmt.Errorf("au moins une carte réseau")
	}
	for i, n := range s.Nets {
		if n.Network == "" {
			return fmt.Errorf("carte %d : réseau manquant", i)
		}
		if n.Address != "" {
			if p, err := netip.ParsePrefix(n.Address); err != nil || !p.Addr().Is4() {
				return fmt.Errorf("IP fixe sans masque ou invalide (ex : 10.10.0.11/24) : %q", n.Address)
			}
		}
		if n.MAC != "" && !reMAC.MatchString(strings.ToLower(n.MAC)) {
			return fmt.Errorf("MAC invalide : %q", n.MAC)
		}
	}
	for _, f := range s.Files {
		if !strings.HasPrefix(f.Dest, "/") {
			return fmt.Errorf("destination de fichier non absolue : %q", f.Dest)
		}
		if f.Mode != "" && !regexp.MustCompile(`^0?[0-7]{3}$`).MatchString(f.Mode) {
			return fmt.Errorf("mode de fichier invalide : %q", f.Mode)
		}
	}
	for _, d := range append([]string{s.Gateway}, s.DNS...) {
		if d != "" {
			if a, err := netip.ParseAddr(d); err != nil || !a.Is4() {
				return fmt.Errorf("adresse invalide : %q", d)
			}
		}
	}
	return nil
}

// RandomMAC renvoie une MAC locale libvirt (52:54:00:xx:xx:xx).
func RandomMAC() string {
	b := make([]byte, 3)
	_, _ = rand.Read(b)
	return fmt.Sprintf("52:54:00:%02x:%02x:%02x", b[0], b[1], b[2])
}

// cloudEnv rassemble ce que la génération cloud-init lit dans l'environnement (réglages, clés, réseaux).
type cloudEnv struct {
	Domain    string // domaine du réseau de la 1re carte
	Gateway   string // passerelle résolue pour une 1re carte en IP fixe
	Keyboard  string
	User      string
	Password  string
	PubKey    string
	RootCA    string // PEM de la CA racine ("" = pas de CA)
	Files     map[string][]byte
	UserData  []byte // contenu du cloud-config fourni (UserData)
	InstantID string
}

// CloudInit est le contenu du disque cloud-init (NoCloud).
type CloudInit struct {
	UserData      string
	NetworkConfig string
	MetaData      string
}

// renderCloudInit produit user-data, network-config et meta-data (même contenu que l'ancien vm-create.sh).
func renderCloudInit(s *VMSpec, e cloudEnv) CloudInit {
	var nc strings.Builder
	nc.WriteString("version: 2\nethernets:\n")
	for i, n := range s.Nets {
		fmt.Fprintf(&nc, "  eth%d:\n    match: {macaddress: %q}\n    set-name: eth%d\n", i, strings.ToLower(n.MAC), i)
		if n.Address == "" {
			nc.WriteString("    dhcp4: true\n    dhcp4-overrides: {use-domains: true}\n")
			continue
		}
		fmt.Fprintf(&nc, "    addresses: [%s]\n", n.Address)
		if i == 0 {
			if e.Gateway != "" {
				fmt.Fprintf(&nc, "    routes:\n      - {to: default, via: %s}\n", e.Gateway)
			}
			dns := s.DNS
			if len(dns) == 0 && e.Gateway != "" {
				dns = []string{e.Gateway}
			}
			if len(dns) > 0 {
				fmt.Fprintf(&nc, "    nameservers: {addresses: [%s]}\n", strings.Join(dns, ", "))
			}
		}
	}

	ci := CloudInit{
		NetworkConfig: nc.String(),
		MetaData:      fmt.Sprintf("instance-id: %s\nlocal-hostname: %s\n", e.InstantID, s.Name),
	}
	if e.UserData != nil {
		ci.UserData = string(e.UserData)
		return ci
	}

	var u strings.Builder
	pkgs := append([]string{"qemu-guest-agent"}, s.Packages...)
	fmt.Fprintf(&u, "#cloud-config\nhostname: %s\nfqdn: %s.%s\nmanage_etc_hosts: true\n", s.Name, s.Name, e.Domain)
	fmt.Fprintf(&u, "users:\n  - name: %s\n    shell: /bin/bash\n    sudo: ALL=(ALL) NOPASSWD:ALL\n    lock_passwd: false\n", e.User)
	fmt.Fprintf(&u, "    plain_text_passwd: %s\n    ssh_authorized_keys:\n      - %s\n", e.Password, strings.TrimSpace(e.PubKey))
	fmt.Fprintf(&u, "ssh_pwauth: false\npackages: [%s]\nruncmd:\n", strings.Join(pkgs, ", "))
	u.WriteString("  - [systemctl, enable, --now, qemu-guest-agent]\n")
	fmt.Fprintf(&u, "  - [sh, -c, \"localectl set-x11-keymap %s 2>/dev/null || true\"]\n", e.Keyboard)
	for _, r := range s.Run {
		fmt.Fprintf(&u, "  - [sh, -c, \"%s\"]\n", strings.ReplaceAll(r, `"`, `\"`))
	}
	// Clavier : /etc/default/keyboard (console et X), puis localectl ci-dessus
	u.WriteString("write_files:\n  - path: /etc/default/keyboard\n    encoding: b64\n")
	fmt.Fprintf(&u, "    content: %s\n", base64.StdEncoding.EncodeToString([]byte(KeyboardFile(e.Keyboard))))
	for _, f := range s.Files {
		mode := f.Mode
		if mode == "" {
			mode = "0644"
		}
		fmt.Fprintf(&u, "  - path: %s\n    permissions: '%s'\n    encoding: b64\n    content: %s\n",
			f.Dest, mode, base64.StdEncoding.EncodeToString(e.Files[f.Src]))
	}
	if !s.NoCA && e.RootCA != "" {
		u.WriteString("ca_certs:\n  trusted:\n    - |\n")
		for _, line := range strings.Split(strings.TrimRight(e.RootCA, "\n"), "\n") {
			fmt.Fprintf(&u, "      %s\n", line)
		}
	}
	ci.UserData = u.String()
	return ci
}

// KeyboardFile renvoie le contenu de /etc/default/keyboard pour une disposition.
func KeyboardFile(layout string) string {
	return fmt.Sprintf("XKBMODEL=\"pc105\"\nXKBLAYOUT=\"%s\"\nXKBVARIANT=\"\"\nXKBOPTIONS=\"\"\nBACKSPACE=\"guess\"\n", layout)
}

func xmlEsc(s string) string {
	var b strings.Builder
	_ = xml.EscapeText(&b, []byte(s))
	return strings.ReplaceAll(b.String(), "'", "&apos;")
}

// renderDomainXML produit la définition libvirt (mêmes périphériques que virt-install : q35, CPU de l'hôte,
// virtio, console série, agent invité, SPICE en local).
func renderDomainXML(s *VMSpec, disk, seed, description string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<domain type='kvm'>\n  <name>%s</name>\n  <description>%s</description>\n", xmlEsc(s.Name), xmlEsc(description))
	if os := Images[s.Image].OSInfo; os != "" {
		fmt.Fprintf(&b, "  <metadata>\n    <libosinfo:libosinfo xmlns:libosinfo=\"http://libosinfo.org/xmlns/libvirt/domain/1.0\">\n      <libosinfo:os id=\"%s\"/>\n    </libosinfo:libosinfo>\n  </metadata>\n", xmlEsc(os))
	}
	fmt.Fprintf(&b, "  <memory unit='MiB'>%d</memory>\n  <currentMemory unit='MiB'>%d</currentMemory>\n  <vcpu placement='static'>%d</vcpu>\n", s.MemoryMiB, s.MemoryMiB, s.CPUs)
	b.WriteString(`  <os>
    <type arch='x86_64' machine='q35'>hvm</type>
    <boot dev='hd'/>
  </os>
  <features><acpi/><apic/><vmport state='off'/></features>
  <cpu mode='host-passthrough' check='none' migratable='on'/>
  <clock offset='utc'>
    <timer name='rtc' tickpolicy='catchup'/>
    <timer name='pit' tickpolicy='delay'/>
    <timer name='hpet' present='no'/>
  </clock>
  <on_poweroff>destroy</on_poweroff>
  <on_reboot>restart</on_reboot>
  <on_crash>destroy</on_crash>
  <pm><suspend-to-mem enabled='no'/><suspend-to-disk enabled='no'/></pm>
  <devices>
`)
	fmt.Fprintf(&b, "    <disk type='file' device='disk'>\n      <driver name='qemu' type='qcow2' discard='unmap'/>\n      <source file='%s'/>\n      <target dev='vda' bus='virtio'/>\n    </disk>\n", xmlEsc(disk))
	fmt.Fprintf(&b, "    <disk type='file' device='cdrom'>\n      <driver name='qemu' type='raw'/>\n      <source file='%s'/>\n      <target dev='sda' bus='sata'/>\n      <readonly/>\n    </disk>\n", xmlEsc(seed))
	for _, n := range s.Nets {
		fmt.Fprintf(&b, "    <interface type='network'>\n      <mac address='%s'/>\n      <source network='%s'/>\n      <model type='virtio'/>\n    </interface>\n", xmlEsc(strings.ToLower(n.MAC)), xmlEsc(n.Network))
	}
	b.WriteString(`    <serial type='pty'><target type='isa-serial' port='0'><model name='isa-serial'/></target></serial>
    <console type='pty'><target type='serial' port='0'/></console>
    <channel type='unix'><target type='virtio' name='org.qemu.guest_agent.0'/></channel>
    <channel type='spicevmc'><target type='virtio' name='com.redhat.spice.0'/></channel>
    <input type='tablet' bus='usb'/>
    <graphics type='spice' autoport='yes'><listen type='address' address='127.0.0.1'/><image compression='off'/></graphics>
    <video><model type='virtio' heads='1' primary='yes'/></video>
    <rng model='virtio'><backend model='random'>/dev/urandom</backend></rng>
    <memballoon model='virtio'/>
  </devices>
</domain>
`)
	return b.String()
}

func instanceID(name string) string { return fmt.Sprintf("%s-%d", name, time.Now().Unix()) }
