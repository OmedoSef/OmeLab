package module

import (
	"os"
	"path/filepath"
	"strings"
)

// Windows : VM installée depuis un ISO Microsoft (labctl ne le télécharge jamais : formulaire d'inscription de
// l'Evaluation Center). Installation sans intervention (autounattend.xml), pilotes virtio, agent invité QEMU et
// OpenSSH Server posés à la fin : la VM se pilote ensuite par SSH (files:, run: en PowerShell) comme une VM Linux.
type Windows struct {
	ISO      string `yaml:"iso"`                // chemin de l'ISO (~ accepté)
	Checksum string `yaml:"checksum,omitempty"` // sha256:… (vérifiée une fois par ISO)
	Edition  string `yaml:"edition"`            // nom de l'image dans install.wim (ex. Windows Server 2022 SERVERSTANDARD)
	Drivers  string `yaml:"drivers,omitempty"`  // dossier des pilotes virtio-win (2k22, 2k19, w11…) ; déduit de l'édition
	Password string `yaml:"password"`           // Administrateur et compte lab (mot de passe de lab, en clair)
	TimeZone string `yaml:"timezone,omitempty"` // fuseau Windows (défaut : Romance Standard Time)
}

// ISOPath renvoie le chemin de l'ISO, ~ développé.
func (w *Windows) ISOPath() string {
	if strings.HasPrefix(w.ISO, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, w.ISO[2:])
		}
	}
	return w.ISO
}

// DriverDir renvoie le dossier virtio-win des pilotes (déclaré, sinon déduit de l'édition ; "" si inconnu).
func (w *Windows) DriverDir() string {
	if w.Drivers != "" {
		return w.Drivers
	}
	e := strings.ToLower(w.Edition)
	for _, k := range []struct{ in, dir string }{
		{"server 2025", "2k25"}, {"server 2022", "2k22"}, {"server 2019", "2k19"}, {"server 2016", "2k16"},
		{"windows 11", "w11"}, {"windows 10", "w10"},
	} {
		if strings.Contains(e, k.in) {
			return k.dir
		}
	}
	return ""
}

// Windows11 : l'édition demande TPM 2.0 et Secure Boot.
func (w *Windows) Windows11() bool { return strings.Contains(strings.ToLower(w.Edition), "windows 11") }

func (c *checker) checkWindows(p string, vm *VM, eff Effective) {
	w, wp := vm.Windows, p+".windows"
	if vm.Image != "" || vm.Source != nil {
		c.err(wp, "windows, image et source s'excluent : choisir l'un des trois")
	}
	if w.ISO == "" {
		c.err(wp+".iso", "chemin de l'ISO obligatoire")
	} else if _, err := os.Stat(w.ISOPath()); err != nil {
		c.err(wp+".iso", "ISO introuvable : %s (à télécharger depuis l'Evaluation Center de Microsoft, puis déposer)", w.ISO)
	}
	if w.Checksum == "" {
		c.warn(wp+".checksum", "pas d'empreinte : l'ISO ne sera pas vérifiée (sha256:…)")
	} else if mm := reChecksum.FindStringSubmatch(strings.ToLower(w.Checksum)); mm == nil || mm[1] != "sha256" || len(mm[2]) != 64 {
		c.err(wp+".checksum", "forme attendue sha256:<64 caractères hexadécimaux>")
	}
	if w.Edition == "" {
		c.err(wp+".edition", "édition obligatoire (nom de l'image dans install.wim, ex. Windows Server 2022 SERVERSTANDARD)")
	} else if w.DriverDir() == "" {
		c.err(wp+".drivers", "pilotes virtio-win non déduits de l'édition : préciser drivers (2k22, 2k19, w11, w10…)")
	}
	if w.Password == "" {
		c.err(wp+".password", "mot de passe obligatoire (Administrateur et compte lab)")
	} else {
		pw := w.Password
		if exp, err := (VarContext{Module: c.m}).Expand(w.Password); err == nil {
			pw = exp
		}
		if len(pw) < 8 || strings.ToLower(pw) == pw {
			c.warn(wp+".password", "mot de passe simple : Windows (et la promotion en contrôleur de domaine) exigent souvent majuscules, chiffres et symboles")
		}
	}
	if len(vm.Packages) > 0 || len(vm.Certificates) > 0 {
		c.err(p, "packages et certificates ne s'appliquent pas à Windows (files: et run: en PowerShell, oui)")
	}
	if eff.Hardware.Firmware != "efi" {
		c.err(p+".hardware.firmware", "Windows s'installe ici en UEFI (firmware: efi)")
	}
	if w.Windows11() && !(eff.Hardware.TPM && eff.Hardware.SecureBoot) {
		c.err(p+".hardware", "Windows 11 demande tpm: true et secure_boot: true")
	}
	if min := map[bool]int{true: 4096, false: 2048}[w.Windows11()]; eff.Memory < min {
		c.warn(p+".memory", "%d Mio : Windows demande au moins %d Mio", eff.Memory, min)
	}
	if eff.Disk < 40 {
		c.warn(p+".disk", "%d Gio : Windows demande au moins 40 Gio (disk: 60 conseillé)", eff.Disk)
	}
}
