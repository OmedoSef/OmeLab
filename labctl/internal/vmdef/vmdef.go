// Package vmdef décrit une VM à créer (Spec) et produit ce que libvirt et la VM reçoivent : cloud-init
// (user-data, network-config, meta-data) et définition XML du domaine. Sans hyperviseur ni réseau (seul osinfo-query
// est consulté sur l'hôte, pour l'identifiant libosinfo) ; la création elle-même (disques, définition, démarrage) est faite par lab.CreateVM.
package vmdef

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/xml"
	"fmt"
	"net/netip"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"labs/labctl/internal/fetch"
	"labs/labctl/internal/names"
)

// Spec décrit une VM à créer depuis une image cloud. C'est la forme générique qu'utilisent
// « labctl vm create » et les VMs des module.yaml.
type Spec struct {
	Name      string
	Image     string // image du catalogue (Images)
	CPUs      int
	MemoryMiB int
	DiskGiB   int
	Nets      []NetSpec // la 1re carte est la carte de gestion
	Gateway   string    // passerelle si la 1re carte a une IP fixe (défaut : hôte du réseau s'il est NAT/routé)
	DNS       []string  // DNS si IP fixe (défaut : la passerelle)
	Lab       string    // rattachement (description libvirt « lab=... »)
	Role      string    // rôle (description libvirt « role=... »)
	Packages  []string  // paquets en plus de qemu-guest-agent
	Keyboard  string    // disposition clavier ; vide = celle de l'hôte (l.Keyboard())
	Files     []FileSpec
	Run       []string     // commandes shell lancées en fin d'installation, dans l'ordre
	UserData  string       // fichier cloud-config remplaçant celui généré
	NoCA      bool         // ne pas injecter la CA racine des labs
	Base      string       // disque fourni (qcow2) à la place d'une image cloud : pas de cloud-init
	HW        Hardware     // matériel virtuel (vide = q35, virtio, spice)
	NoAgent   bool         // pas de canal d'agent invité
	Install   *InstallSpec // installation depuis des ISO (Windows) : disque vierge, démarrage sur le 1er ISO
}

// InstallSpec : installation depuis des ISO (ex. Windows, pilotes virtio, réponses), sans cloud-init.
type InstallSpec struct {
	ISOs []string // lecteurs, dans l'ordre ; le premier est amorçable
}

// Hardware : matériel virtuel d'une VM ; une valeur vide prend le défaut indiqué.
type Hardware struct {
	Machine    string // q35 | pc
	DiskBus    string // virtio | sata | ide
	NICModel   string // virtio | e1000
	Graphics   string // spice | vnc | none
	Video      string // virtio | vga | qxl | cirrus
	OSInfo     string // identifiant libosinfo (défaut : celui de l'image)
	Firmware   string // bios (défaut) | efi
	SecureBoot bool   // efi : Secure Boot (clés Microsoft enrôlées)
	TPM        bool   // TPM 2.0 émulé (swtpm)
}

func or(v, def string) string {
	if v == "" {
		return def
	}
	return v
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
var reFileMode = regexp.MustCompile(`^0?[0-7]{3}$`) // droits d'un fichier copié (ex. 0644)

// Validate contrôle une description de VM avant toute création.
func (s *Spec) Validate() error {
	switch {
	case !names.Host(s.Name):
		return fmt.Errorf("nom invalide (lettres, chiffres, tirets) : %q", s.Name)
	case s.Base == "" && s.Install == nil && fetch.Images[s.Image].URL == "":
		return fmt.Errorf("image inconnue : %q (%s)", s.Image, strings.Join(fetch.ImageNames(), ", "))
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
		if n.MAC != "" && !names.MAC(strings.ToLower(n.MAC)) {
			return fmt.Errorf("MAC invalide : %q", n.MAC)
		}
	}
	for _, f := range s.Files {
		if !strings.HasPrefix(f.Dest, "/") {
			return fmt.Errorf("destination de fichier non absolue : %q", f.Dest)
		}
		if f.Mode != "" && !reFileMode.MatchString(f.Mode) {
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

// CloudEnv rassemble ce que la génération cloud-init lit dans l'environnement (réglages, clés, réseaux).
type CloudEnv struct {
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

// RenderCloudInit produit user-data, network-config et meta-data (même contenu que l'ancien vm-create.sh).
func RenderCloudInit(s *Spec, e CloudEnv) CloudInit {
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
	// Clavier de l'hôte (ou override VM) : X11 ET console appliqués tout de suite (setupcon), pas seulement au prochain démarrage
	fmt.Fprintf(&u, "  - [sh, -c, \"localectl set-x11-keymap %s 2>/dev/null; setupcon --save --force 2>/dev/null; true\"]\n", e.Keyboard)
	for _, r := range s.Run {
		fmt.Fprintf(&u, "  - [sh, -c, \"%s\"]\n", strings.ReplaceAll(r, `"`, `\"`))
	}
	// Clavier : /etc/default/keyboard (console) ET /etc/X11/xorg.conf.d (X), lus au démarrage sans dépendre de
	// localectl (qui peut échouer en plein cloud-init) ; le runcmd plus haut applique en plus le tout à chaud.
	u.WriteString("write_files:\n  - path: /etc/default/keyboard\n    encoding: b64\n")
	fmt.Fprintf(&u, "    content: %s\n", base64.StdEncoding.EncodeToString([]byte(KeyboardFile(e.Keyboard))))
	u.WriteString("  - path: /etc/X11/xorg.conf.d/00-keyboard.conf\n    encoding: b64\n")
	fmt.Fprintf(&u, "    content: %s\n", base64.StdEncoding.EncodeToString([]byte(XkbConf(e.Keyboard))))
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

// XkbConf renvoie une règle Xorg (/etc/X11/xorg.conf.d/00-keyboard.conf) fixant la disposition du serveur X,
// lue au démarrage de X indépendamment de localectl.
func XkbConf(layout string) string {
	return fmt.Sprintf("Section \"InputClass\"\n    Identifier \"system-keyboard\"\n    MatchIsKeyboard \"on\"\n    Option \"XkbLayout\" \"%s\"\nEndSection\n", layout)
}

func XMLEsc(s string) string {
	var b strings.Builder
	_ = xml.EscapeText(&b, []byte(s))
	return strings.ReplaceAll(b.String(), "'", "&apos;")
}

// RenderDomainXML produit la définition libvirt (mêmes périphériques que virt-install : q35, CPU de l'hôte,
// virtio, console série, agent invité, SPICE en local). seed vide : pas de disque cloud-init.
func RenderDomainXML(s *Spec, disk, seed, description string) string {
	hw := s.HW
	machine, bus, nic := or(hw.Machine, "q35"), or(hw.DiskBus, "virtio"), or(hw.NICModel, "virtio")
	graphics, video := or(hw.Graphics, "spice"), or(hw.Video, "virtio")
	osinfo := or(hw.OSInfo, fetch.Images[s.Image].OSInfo)
	var b strings.Builder
	fmt.Fprintf(&b, "<domain type='kvm'>\n  <name>%s</name>\n  <description>%s</description>\n", XMLEsc(s.Name), XMLEsc(description))
	if osinfo != "" {
		fmt.Fprintf(&b, "  <metadata>\n    <libosinfo:libosinfo xmlns:libosinfo=\"http://libosinfo.org/xmlns/libvirt/domain/1.0\">\n      <libosinfo:os id=\"%s\"/>\n    </libosinfo:libosinfo>\n  </metadata>\n", XMLEsc(osinfoURI(osinfo)))
	}
	fmt.Fprintf(&b, "  <memory unit='MiB'>%d</memory>\n  <currentMemory unit='MiB'>%d</currentMemory>\n  <vcpu placement='static'>%d</vcpu>\n", s.MemoryMiB, s.MemoryMiB, s.CPUs)
	bootCD := ""
	if s.Install != nil {
		bootCD = "<boot dev='cdrom'/>\n    "
	}
	if hw.Firmware == "efi" {
		// OVMF choisi par libvirt (descripteurs de firmware)
		sb := map[bool]string{true: "yes", false: "no"}[hw.SecureBoot]
		fmt.Fprintf(&b, "  <os firmware='efi'>\n    <type arch='x86_64' machine='%s'>hvm</type>\n    <firmware>\n      <feature enabled='%s' name='secure-boot'/>\n      <feature enabled='%s' name='enrolled-keys'/>\n    </firmware>\n    %s<boot dev='hd'/>\n  </os>\n", XMLEsc(machine), sb, sb, bootCD)
	} else {
		fmt.Fprintf(&b, "  <os>\n    <type arch='x86_64' machine='%s'>hvm</type>\n    %s<boot dev='hd'/>\n  </os>\n", XMLEsc(machine), bootCD)
	}
	smm := ""
	if hw.SecureBoot {
		smm = "<smm state='on'/>"
	}
	b.WriteString(`  <features><acpi/><apic/><vmport state='off'/>` + smm + `</features>
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
	dev := map[string]string{"virtio": "vda", "sata": "sda", "ide": "hda"}[bus]
	fmt.Fprintf(&b, "    <disk type='file' device='disk'>\n      <driver name='qemu' type='qcow2' discard='unmap'/>\n      <source file='%s'/>\n      <target dev='%s' bus='%s'/>\n    </disk>\n", XMLEsc(disk), XMLEsc(dev), XMLEsc(bus))
	if s.Install != nil { // lecteurs SATA (q35) : ISO d'installation amorçable, puis les autres
		for i, iso := range s.Install.ISOs {
			fmt.Fprintf(&b, "    <disk type='file' device='cdrom'>\n      <driver name='qemu' type='raw'/>\n      <source file='%s'/>\n      <target dev='sd%c' bus='sata'/>\n      <readonly/>\n    </disk>\n", XMLEsc(iso), 'a'+i)
		}
	}
	if seed != "" {
		cdBus, cdDev := "sata", "sdb"
		if bus == "virtio" {
			cdDev = "sda"
		}
		if machine == "pc" { // i440fx : pas de SATA
			cdBus, cdDev = "ide", "hdc"
		}
		fmt.Fprintf(&b, "    <disk type='file' device='cdrom'>\n      <driver name='qemu' type='raw'/>\n      <source file='%s'/>\n      <target dev='%s' bus='%s'/>\n      <readonly/>\n    </disk>\n", XMLEsc(seed), XMLEsc(cdDev), XMLEsc(cdBus))
	}
	for _, n := range s.Nets {
		fmt.Fprintf(&b, "    <interface type='network'>\n      <mac address='%s'/>\n      <source network='%s'/>\n      <model type='%s'/>\n    </interface>\n", XMLEsc(strings.ToLower(n.MAC)), XMLEsc(n.Network), XMLEsc(nic))
	}
	b.WriteString("    <serial type='pty'><target type='isa-serial' port='0'><model name='isa-serial'/></target></serial>\n    <console type='pty'><target type='serial' port='0'/></console>\n")
	if !s.NoAgent {
		b.WriteString("    <channel type='unix'><target type='virtio' name='org.qemu.guest_agent.0'/></channel>\n")
	}
	switch graphics {
	case "spice":
		b.WriteString("    <channel type='spicevmc'><target type='virtio' name='com.redhat.spice.0'/></channel>\n    <input type='tablet' bus='usb'/>\n")
		b.WriteString("    <graphics type='spice' autoport='yes'><listen type='address' address='127.0.0.1'/><image compression='off'/></graphics>\n")
	case "vnc":
		b.WriteString("    <input type='tablet' bus='usb'/>\n    <graphics type='vnc' autoport='yes'><listen type='address' address='127.0.0.1'/></graphics>\n")
	}
	if graphics != "none" {
		fmt.Fprintf(&b, "    <video><model type='%s' heads='1' primary='yes'/></video>\n", XMLEsc(video))
	}
	if hw.TPM {
		b.WriteString("    <tpm model='tpm-crb'><backend type='emulator' version='2.0'/></tpm>\n")
	}
	b.WriteString(`    <rng model='virtio'><backend model='random'>/dev/urandom</backend></rng>
    <memballoon model='virtio'/>
  </devices>
</domain>
`)
	return b.String()
}

// osinfoURI accepte un identifiant court (debian13) ou complet (http://debian.org/debian/13).
func osinfoURI(id string) string {
	if strings.Contains(id, "://") {
		return id
	}
	if out, err := exec.Command("osinfo-query", "os", "--fields=short-id,id").Output(); err == nil {
		for _, line := range strings.Split(string(out), "\n") {
			f := strings.Split(line, "|")
			if len(f) == 2 && strings.TrimSpace(f[0]) == id {
				return strings.TrimSpace(f[1])
			}
		}
	}
	return id
}

func InstanceID(name string) string { return fmt.Sprintf("%s-%d", name, time.Now().Unix()) }
