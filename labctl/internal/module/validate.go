package module

import (
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// NetInfo décrit un réseau déjà utilisé (autre module ou réseau libvirt), pour les conflits.
type NetInfo struct {
	Owner  string // « module demo » ou « réseau libvirt default »
	Name   string // nom du réseau libvirt
	Subnet netip.Prefix
	Domain string
}

// Context : ce que la validation doit connaître hors du fichier.
type Context struct {
	RootDomain string
	Images     []string            // catalogue des images cloud
	Modules    []string            // modules existants (pour les liens)
	Targets    map[string][]string // module -> VMs à IP fixe et réservations (cibles de connections:)
	Used       []NetInfo           // réseaux et domaines déjà pris par d'autres
	HostRAM    int                 // Mio (0 = inconnu)
	Settings   Settings
}

var (
	reWinPath  = regexp.MustCompile(`^[A-Za-z]:[/\\]`)
	reName     = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)
	reVMName   = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)
	reMAC      = regexp.MustCompile(`^([0-9a-f]{2}:){5}[0-9a-f]{2}$`)
	reDomain   = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]*[a-z0-9])?\.)+[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)
	reChecksum = regexp.MustCompile(`^(md5|sha1|sha256|sha512):([0-9a-fA-F]+)$`)
	reSnapshot = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
	reMode     = regexp.MustCompile(`^0?[0-7]{3}$`)
	hexLen     = map[string]int{"md5": 32, "sha1": 40, "sha256": 64, "sha512": 128}
	enums      = map[string][]string{
		"internet": {"off", "install", "never"}, "hardware.machine": {"q35", "pc"},
		"hardware.disk_bus": {"virtio", "sata", "ide"}, "hardware.nic_model": {"virtio", "e1000"},
		"hardware.firmware": {"bios", "efi"}, "hardware.graphics": {"spice", "vnc", "none"}, "hardware.video": {"virtio", "vga", "qxl", "cirrus"},
	}
)

// ReservedNames sont des sous-commandes de « labctl module » ou de labctl : pas utilisables comme nom de module.
var ReservedNames = []string{"list", "validate", "show", "env", "conf", "edit", "core", "module", "vm", "net", "image", "pki", "status", "gui", "hostfw", "completion", "help"}

type checker struct {
	m      *Module
	issues []Issue
}

func (c *checker) err(path, format string, a ...any) {
	c.issues = append(c.issues, Issue{Error: true, Path: path, Line: c.m.Line(path), Msg: fmt.Sprintf(format, a...)})
}

func (c *checker) warn(path, format string, a ...any) {
	c.issues = append(c.issues, Issue{Path: path, Line: c.m.Line(path), Msg: fmt.Sprintf(format, a...)})
}

// enum vérifie qu'une valeur fait partie des valeurs permises pour la clé key (ex. hardware.machine).
func (c *checker) enum(path, key, value string) {
	if value == "" {
		return
	}
	for _, v := range enums[key] {
		if v == value {
			return
		}
	}
	c.err(path, "valeur %q invalide (%s)", value, strings.Join(enums[key], ", "))
}

// Validate contrôle un module sans rien créer. Les problèmes sont triés par ligne.
func (m *Module) Validate(ctx Context) []Issue {
	c := &checker{m: m}

	// --- module
	switch {
	case m.Module == "":
		c.err("module", "nom du module obligatoire")
	case !reName.MatchString(m.Module):
		c.err("module", "nom invalide %q (minuscules, chiffres, tirets)", m.Module)
	case len(m.Module) > 9:
		c.err("module", "nom trop long %q : 9 caractères au plus (pont réseau virbr-<module>, 15 caractères)", m.Module)
	case contains(ReservedNames, m.Module):
		c.err("module", "nom réservé %q (commande de labctl : %s)", m.Module, strings.Join(ReservedNames, ", "))
	case m.Dir != "" && filepath.Base(m.Dir) != m.Module:
		c.err("module", "le nom %q doit être celui du dossier (%s)", m.Module, filepath.Base(m.Dir))
	}

	// --- réseau
	net := m.NetworkName()
	domain := m.Domain(ctx.RootDomain)
	if m.Network != nil {
		if m.Network.Name != "" && !reName.MatchString(m.Network.Name) {
			c.err("network.name", "nom invalide %q", m.Network.Name)
		}
		if m.Network.Subnet != "" {
			p, err := netip.ParsePrefix(m.Network.Subnet)
			switch {
			case err != nil || !p.Addr().Is4():
				c.err("network.subnet", "sous-réseau IPv4 invalide %q (ex. 10.66.66.0/24)", m.Network.Subnet)
			case p.Bits() != 24:
				c.err("network.subnet", "les modules utilisent un /24 (convention .1 hôte, .2-.99 fixes, .100-.199 DHCP, .254 passerelle)")
			case p.Masked() != p:
				c.err("network.subnet", "%s n'est pas une adresse de réseau (attendu %s)", p, p.Masked())
			}
		}
		if m.Network.Domain != "" && !reDomain.MatchString(m.Network.Domain) {
			c.err("network.domain", "domaine invalide %q", m.Network.Domain)
		}
		if m.Network.Subdomain != "" && !reName.MatchString(m.Network.Subdomain) {
			c.err("network.subdomain", "sous-domaine invalide %q (un seul libellé, ex. pentest)", m.Network.Subdomain)
		}
		if m.Network.Domain != "" && m.Network.Subdomain != "" {
			c.warn("network.subdomain", "domaine et subdomain renseignés tous les deux : subdomain ignoré (domain l'emporte)")
		}
	}
	if m.Network == nil || m.Network.Subnet == "" {
		c.warn("network", "pas de sous-réseau : un /24 libre sera choisi et écrit dans le fichier au premier « up »")
	}
	sub := m.Subnet()
	for _, u := range ctx.Used {
		if sub.IsValid() && u.Subnet.IsValid() && sub.Overlaps(u.Subnet) {
			c.err("network.subnet", "%s chevauche %s (%s, %s)", sub, u.Subnet, u.Owner, u.Name)
		}
		if u.Name != "" && u.Name == net {
			c.err("network.name", "réseau %s déjà utilisé par %s", net, u.Owner)
		}
		if u.Domain != "" && u.Domain == domain {
			c.err("network.domain", "domaine %s déjà utilisé par %s", domain, u.Owner)
		}
	}

	// --- PKI
	if m.PKI != nil && !reName.MatchString(m.PKI.CA) {
		c.err("pki.ca", "nom de CA invalide %q", m.PKI.CA)
	}

	// --- VMs : noms, IP, MAC
	ips, macs := map[int]string{}, map[string]string{}
	claim := func(path, who string, ip int, mac string) {
		if ip != 0 {
			if ip < 2 || ip > 99 {
				c.err(path+".ip", "IP fixe entre 2 et 99 (.100-.199 : DHCP libre ; .254 : passerelle)")
			} else if other, ok := ips[ip]; ok {
				c.err(path+".ip", "%d déjà utilisé par %s", ip, other)
			} else {
				ips[ip] = who
			}
		}
		if mac != "" {
			mac = strings.ToLower(mac)
			if !reMAC.MatchString(mac) {
				c.err(path+".mac", "MAC invalide %q", mac)
			} else if other, ok := macs[mac]; ok {
				c.err(path+".mac", "MAC déjà utilisée par %s", other)
			} else if strings.HasPrefix(mac, "52:54:00:fe:") {
				c.err(path+".mac", "52:54:00:fe:… est réservé aux cartes de la passerelle core")
			} else {
				macs[mac] = who
			}
		}
	}
	if len(m.VMs) == 0 {
		c.warn("vms", "aucune VM")
	}
	totalMem := 0
	for _, name := range m.VMNames() {
		vm := m.VMs[name]
		p := "vms." + name
		if !reVMName.MatchString(name) {
			c.err(p, "nom de VM invalide %q (minuscules, chiffres, tirets)", name)
		}
		claim(p, p, vm.IP, vm.MAC)
		c.checkVM(name, vm, ctx)
		totalMem += m.Effective(name, ctx.Settings).Memory
	}
	for i, h := range m.DNS.Hosts {
		p := fmt.Sprintf("dns.hosts.%d", i)
		if !reVMName.MatchString(h.Name) {
			c.err(p+".name", "nom invalide %q", h.Name)
		}
		if _, isVM := m.VMs[h.Name]; isVM {
			c.err(p+".name", "%s est déjà une VM du module", h.Name)
		}
		if h.IP == 0 || h.MAC == "" {
			c.err(p, "une réservation demande ip et mac")
		}
		claim(p, p, h.IP, h.MAC)
	}
	for i, a := range m.Attach {
		p := fmt.Sprintf("attach.%d", i)
		mod, vm := a.Split()
		switch {
		case mod == "" || vm == "" || strings.Contains(vm, "/"):
			c.err(p+".vm", "attendu <module>/<vm> (ex. attacker/kali), pas %q", a.VM)
		case mod == m.Module:
			c.err(p+".vm", "%s est déjà une VM de ce module", vm)
		case !contains(ctx.Modules, mod):
			c.err(p+".vm", "module inconnu %q", mod)
		case ctx.Targets != nil && !contains(ctx.Targets[mod], vm):
			c.err(p+".vm", "%s n'est pas une VM à IP fixe du module %s", vm, mod)
		}
		if _, isVM := m.VMs[vm]; isVM || m.hasHost(vm) {
			c.err(p+".vm", "le nom %s est déjà pris dans ce module (VM ou réservation) : il servirait deux fois dans le DNS", vm)
		}
		if a.IP == 0 {
			c.err(p+".ip", "ip obligatoire (dernier octet réservé sur le réseau de ce module)")
		}
		claim(p, p, a.IP, a.MAC)
	}
	if ctx.HostRAM > 0 && totalMem > ctx.HostRAM {
		c.warn("vms", "mémoire totale des VMs (%d Mio) supérieure à celle de l'hôte (%d Mio)", totalMem, ctx.HostRAM)
	}

	// --- DNS : alias et liens
	for alias, target := range m.DNS.Aliases {
		p := "dns.aliases." + alias
		if !reVMName.MatchString(alias) {
			c.err(p, "nom d'alias invalide %q", alias)
		}
		if _, isVM := m.VMs[alias]; isVM {
			c.err(p, "%s est déjà le nom d'une VM", alias)
		}
		if !strings.Contains(target, ".") && m.VMs[target] == nil && !m.hasHost(target) {
			c.err(p, "cible inconnue %q (VM du module, réservation, ou nom complet)", target)
		}
	}
	for i, l := range m.DNS.Links {
		p := fmt.Sprintf("dns.links.%d", i)
		switch {
		case l == m.Module:
			c.err(p, "un module est toujours relié à lui-même")
		case !contains(ctx.Modules, l):
			c.err(p, "module inconnu %q", l)
		}
	}
	c.checkConnections(ctx)

	sort.SliceStable(c.issues, func(i, j int) bool { return c.issues[i].Line < c.issues[j].Line })
	return c.issues
}

func (c *checker) checkVM(name string, vm *VM, ctx Context) {
	m, p := c.m, "vms."+name
	cloud := vm.Source == nil && vm.Windows == nil // image cloud : cloud-init disponible
	if vm.Windows != nil {
		c.checkWindows(p, vm, m.Effective(name, ctx.Settings))
	}
	if vm.Image != "" && vm.Source != nil {
		c.err(p, "image et source à la fois : choisir l'une (image cloud) ou l'autre (disque fourni)")
	}
	eff := m.Effective(name, ctx.Settings)
	if cloud && !contains(ctx.Images, eff.Image) {
		c.err(p+".image", "image inconnue %q (%s)", eff.Image, strings.Join(ctx.Images, ", "))
	}
	if s := vm.Source; s != nil {
		sp := p + ".source"
		switch {
		case s.URL != "" && s.Sums != "":
			c.err(sp, "url et sums à la fois : url + checksum, ou sums + file")
		case s.URL != "":
			if !strings.HasPrefix(s.URL, "https://") {
				c.err(sp+".url", "adresse https:// attendue")
			}
			if s.Checksum == "" {
				c.err(sp+".checksum", "empreinte obligatoire (ex. sha256:…)")
			}
		case s.Sums != "":
			if !strings.HasPrefix(s.Sums, "https://") {
				c.err(sp+".sums", "adresse https:// attendue")
			}
			if s.File == "" {
				c.err(sp+".file", "nom du fichier à chercher dans sums obligatoire")
			} else if strings.Contains(s.File, "*") {
				c.warn(sp+".file", "version non figée (*) : la dernière publiée sera prise")
			}
		default:
			c.err(sp, "url + checksum, ou sums + file")
		}
		if s.Checksum != "" {
			mm := reChecksum.FindStringSubmatch(strings.ToLower(s.Checksum))
			switch {
			case mm == nil:
				c.err(sp+".checksum", "forme attendue algo:empreinte (md5, sha1, sha256, sha512)")
			case len(mm[2]) != hexLen[mm[1]]:
				c.err(sp+".checksum", "empreinte %s de longueur incorrecte", mm[1])
			case mm[1] == "md5" || mm[1] == "sha1":
				c.warn(sp+".checksum", "%s seulement : empreinte faible (garder si c'est la seule publiée)", strings.ToUpper(mm[1]))
			}
		}
	}

	// cloud-init seulement (Windows : files et run passent par SSH)
	if !cloud && vm.Windows == nil {
		for _, f := range []struct {
			path string
			set  bool
		}{{"packages", len(vm.Packages) > 0}, {"files", len(vm.Files) > 0}, {"run", len(vm.Run) > 0}, {"certificates", len(vm.Certificates) > 0}} {
			if f.set {
				c.err(p+"."+f.path, "demande une image cloud (cloud-init et accès SSH) : pas disponible avec source")
			}
		}
	}
	for _, f := range []struct{ path, val string }{
		{"internet", vm.Internet}, {"hardware.machine", vm.Hardware.Machine}, {"hardware.disk_bus", vm.Hardware.DiskBus},
		{"hardware.nic_model", vm.Hardware.NICModel}, {"hardware.graphics", vm.Hardware.Graphics}, {"hardware.video", vm.Hardware.Video},
		{"hardware.firmware", vm.Hardware.Firmware},
	} {
		c.enum(p+"."+f.path, f.path, f.val)
	}
	if hw := m.Effective(name, ctx.Settings).Hardware; hw.SecureBoot && (hw.Firmware != "efi" || hw.Machine != "q35") {
		c.err(p+".hardware.secure_boot", "Secure Boot demande firmware: efi (et machine q35)")
	}
	if vm.CPUs < 0 || vm.Memory < 0 || vm.Disk < 0 {
		c.err(p, "ressources négatives")
	}
	if vm.Memory > 0 && vm.Memory < 256 {
		c.err(p+".memory", "au moins 256 Mio")
	}
	if eff.Internet == "never" && vm.IP == 0 {
		c.err(p+".internet", "internet: never demande une IP fixe (ip:) : la passerelle bloque la VM par son adresse")
	}
	if eff.Internet == "never" && len(vm.Packages) > 0 {
		c.err(p+".packages", "internet: never : les paquets ne pourront pas être installés")
	}
	if vm.Role == "target" && eff.Internet == "install" {
		c.warn(p+".internet", "cible vulnérable avec Internet pendant l'installation : vérifier que c'est voulu")
	}
	if eff.Snapshot == "" {
		c.warn(p+".snapshot", "pas de snapshot de référence (snapshot: clean) pour revenir à l'état initial")
	} else if !reSnapshot.MatchString(eff.Snapshot) {
		c.err(p+".snapshot", "nom de snapshot invalide %q", eff.Snapshot)
	}

	vars := VarContext{Module: m, VM: name, Root: ctx.RootDomain, Keyboard: "fr"}
	checkVars := func(path, text string) {
		if _, err := vars.Expand(text); err != nil && !strings.Contains(err.Error(), "encore inconnu") {
			c.err(path, "%v", err)
		}
	}
	for i, f := range vm.Files {
		fp := fmt.Sprintf("%s.files.%d", p, i)
		if f.Src == "" || f.Dest == "" {
			c.err(fp, "src et dest obligatoires")
			continue
		}
		if winPath := vm.Windows != nil && reWinPath.MatchString(f.Dest); !strings.HasPrefix(f.Dest, "/") && !winPath {
			c.err(fp+".dest", "chemin absolu attendu (Windows : C:/dossier/fichier)")
		}
		if f.Mode != "" && !reMode.MatchString(f.Mode) {
			c.err(fp+".mode", "mode invalide %q (ex. 0644)", f.Mode)
		}
		src, err := SourcePath(m.Dir, f.Src)
		if err != nil {
			c.err(fp+".src", "%v", err)
			continue
		}
		b, err := os.ReadFile(src)
		if err != nil {
			c.err(fp+".src", "fichier introuvable : %s", f.Src)
		} else if f.Template {
			checkVars(fp+".src", string(b))
		}
	}
	for i, r := range vm.Run {
		checkVars(fmt.Sprintf("%s.run.%d", p, i), r)
	}
	for i, ct := range vm.Certificates {
		cp := fmt.Sprintf("%s.certificates.%d", p, i)
		if m.PKI == nil {
			c.err(cp, "certificat sans CA : ajouter pki: {ca: …} au module")
		}
		if !reVMName.MatchString(ct.Name) {
			c.err(cp+".name", "nom invalide %q", ct.Name)
		}
		if !strings.HasPrefix(ct.Cert, "/") || !strings.HasPrefix(ct.Key, "/") {
			c.err(cp, "cert et key : chemins absolus dans la VM obligatoires")
		}
		if ct.IP && vm.IP == 0 {
			c.err(cp+".ip", "ip: true demande une IP fixe pour la VM")
		}
		dom := m.Domain(ctx.RootDomain)
		for j, d := range ct.DNS {
			if strings.Contains(d, ".") && d != dom && !strings.HasSuffix(d, "."+dom) {
				c.err(fmt.Sprintf("%s.dns.%d", cp, j), "%s hors du domaine de la CA du module (%s) : sera refusé", d, dom)
			}
		}
		checkVars(cp+".reload", ct.Reload)
	}
}

func (m *Module) hasHost(name string) bool {
	for _, h := range m.DNS.Hosts {
		if h.Name == name {
			return true
		}
	}
	return false
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// Errors compte les erreurs d'une liste de problèmes.
func Errors(issues []Issue) int {
	n := 0
	for _, i := range issues {
		if i.Error {
			n++
		}
	}
	return n
}
