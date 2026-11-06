package virt

import (
	"encoding/xml"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/digitalocean/go-libvirt"
)

// DefineDomain enregistre une VM à partir de sa définition XML.
func (c *Conn) DefineDomain(xmlDesc string) error {
	return c.do(func(lv *libvirt.Libvirt) error {
		_, err := lv.DomainDefineXML(xmlDesc)
		return err
	})
}

// StartDomain démarre une VM.
func (c *Conn) StartDomain(name string) error {
	return c.do(func(lv *libvirt.Libvirt) error {
		d, err := c.lookup(lv, name)
		if err != nil {
			return err
		}
		return lv.DomainCreate(d)
	})
}

// DestroyDomain arrête une VM immédiatement (équivalent d'une coupure d'alimentation).
func (c *Conn) DestroyDomain(name string) error {
	return c.do(func(lv *libvirt.Libvirt) error {
		d, err := c.lookup(lv, name)
		if err != nil {
			return err
		}
		return lv.DomainDestroy(d)
	})
}

// UndefineDomain supprime la définition d'une VM, avec son état sauvegardé, ses métadonnées de snapshots
// et sa NVRAM éventuelle (les fichiers disques ne sont pas touchés).
func (c *Conn) UndefineDomain(name string) error {
	return c.do(func(lv *libvirt.Libvirt) error {
		d, err := c.lookup(lv, name)
		if err != nil {
			return err
		}
		return lv.DomainUndefineFlags(d, libvirt.DomainUndefineManagedSave|libvirt.DomainUndefineSnapshotsMetadata|
			libvirt.DomainUndefineNvram|libvirt.DomainUndefineCheckpointsMetadata)
	})
}

// Disks renvoie les fichiers disques et CD d'une VM.
func (c *Conn) Disks(name string) ([]string, error) {
	var out []string
	err := c.do(func(lv *libvirt.Libvirt) error {
		d, err := c.lookup(lv, name)
		if err != nil {
			return err
		}
		x, err := lv.DomainGetXMLDesc(d, libvirt.DomainXMLInactive)
		if err != nil {
			return err
		}
		var dx struct {
			Disks []struct {
				Source struct {
					File string `xml:"file,attr"`
				} `xml:"source"`
			} `xml:"devices>disk"`
		}
		if err := xml.Unmarshal([]byte(x), &dx); err != nil {
			return err
		}
		out = nil
		for _, dk := range dx.Disks {
			if dk.Source.File != "" {
				out = append(out, dk.Source.File)
			}
		}
		return nil
	})
	return out, err
}

// RemovePoolsAt supprime les pools de stockage libvirt dont le dossier est dir (virt-install en créait un
// par dossier de VM). Renvoie les pools retirés.
func (c *Conn) RemovePoolsAt(dir string) ([]string, error) {
	var removed []string
	err := c.do(func(lv *libvirt.Libvirt) error {
		pools, _, err := lv.ConnectListAllStoragePools(1, 0)
		if err != nil {
			return err
		}
		for _, p := range pools {
			x, err := lv.StoragePoolGetXMLDesc(p, 0)
			if err != nil {
				continue
			}
			var px struct {
				Path string `xml:"target>path"`
			}
			if xml.Unmarshal([]byte(x), &px) != nil || filepath.Clean(px.Path) != filepath.Clean(dir) {
				continue
			}
			if a, _ := lv.StoragePoolIsActive(p); a == 1 {
				_ = lv.StoragePoolDestroy(p)
			}
			if err := lv.StoragePoolUndefine(p); err != nil {
				return fmt.Errorf("pool %s : %w", p.Name, err)
			}
			removed = append(removed, p.Name)
		}
		return nil
	})
	return removed, err
}

// Description renvoie la description libvirt d'une VM ("" si aucune).
func (c *Conn) Description(name string) string {
	desc := ""
	_ = c.do(func(lv *libvirt.Libvirt) error {
		d, err := c.lookup(lv, name)
		if err != nil {
			return err
		}
		desc, _ = lv.DomainGetMetadata(d, int32(libvirt.DomainMetadataDescription), nil, 0)
		return nil
	})
	return strings.TrimSpace(desc)
}

// ShutdownDomain demande l'arrêt propre d'une VM (ACPI).
func (c *Conn) ShutdownDomain(name string) error {
	return c.do(func(lv *libvirt.Libvirt) error {
		d, err := c.lookup(lv, name)
		if err != nil {
			return err
		}
		return lv.DomainShutdown(d)
	})
}

// RebootDomain redémarre une VM (ACPI).
func (c *Conn) RebootDomain(name string) error {
	return c.do(func(lv *libvirt.Libvirt) error {
		d, err := c.lookup(lv, name)
		if err != nil {
			return err
		}
		return lv.DomainReboot(d, 0)
	})
}

// IsEFI : la VM démarre-t-elle sur un firmware UEFI (pflash) ? Ses snapshots internes se prennent alors
// VM arrêtée (limite de libvirt).
func (c *Conn) IsEFI(name string) bool {
	x, err := c.DomainXML(name, true)
	return err == nil && (strings.Contains(x, "firmware='efi'") || strings.Contains(x, "<loader"))
}

// DomainXML renvoie la définition d'une VM : celle du prochain démarrage (inactive) ou celle en cours.
func (c *Conn) DomainXML(name string, inactive bool) (string, error) {
	var x string
	err := c.do(func(lv *libvirt.Libvirt) error {
		d, err := c.lookup(lv, name)
		if err != nil {
			return err
		}
		var flags libvirt.DomainXMLFlags
		if inactive {
			flags = libvirt.DomainXMLInactive
		}
		x, err = lv.DomainGetXMLDesc(d, flags)
		return err
	})
	return x, err
}

// Resources renvoie les vCPU et la mémoire (Mio) du prochain démarrage d'une VM.
func (c *Conn) Resources(name string) (cpus, memMiB int, err error) {
	x, err := c.DomainXML(name, true)
	if err != nil {
		return 0, 0, err
	}
	var d struct {
		VCPU   int `xml:"vcpu"`
		Memory struct {
			Unit  string `xml:"unit,attr"`
			Value int    `xml:",chardata"`
		} `xml:"memory"`
	}
	if err := xml.Unmarshal([]byte(x), &d); err != nil {
		return 0, 0, err
	}
	mem := d.Memory.Value
	switch strings.ToLower(d.Memory.Unit) {
	case "", "kib", "k":
		mem /= 1024
	case "gib", "g":
		mem *= 1024
	case "b", "bytes":
		mem /= 1024 * 1024
	}
	return d.VCPU, mem, nil
}

// SetResources change vCPU et mémoire du prochain démarrage (la VM doit être redémarrée pour les prendre).
func (c *Conn) SetResources(name string, cpus, memMiB int) error {
	x, err := c.DomainXML(name, true)
	if err != nil {
		return err
	}
	x = reVCPU.ReplaceAllString(x, fmt.Sprintf("${1}%d${2}", cpus))
	x = reMemory.ReplaceAllString(x, fmt.Sprintf("<${1} unit='KiB'>%d</${1}>", memMiB*1024))
	return c.DefineDomain(x)
}

var (
	reVCPU   = regexp.MustCompile(`(<vcpu[^>]*>)\d+(</vcpu>)`)
	reMemory = regexp.MustCompile(`<(memory|currentMemory)[^>]*>\d+</(?:memory|currentMemory)>`)
)

// DeleteVolumeAt supprime un disque appartenant à un pool libvirt (fichiers de root : pas de sudo).
// Renvoie false si le fichier n'appartient à aucun pool.
func (c *Conn) DeleteVolumeAt(path string) (bool, error) {
	found := false
	err := c.do(func(lv *libvirt.Libvirt) error {
		v, err := lv.StorageVolLookupByPath(path)
		if err != nil {
			return nil // pas un volume connu de libvirt
		}
		found = true
		return lv.StorageVolDelete(v, 0)
	})
	return found, err
}

// AttachInterface ajoute une carte sur un réseau libvirt (définition, et VM en marche si live).
func (c *Conn) AttachInterface(name, network, mac, model string, live bool) error {
	var b strings.Builder
	b.WriteString("<interface type='network'><mac address='")
	_ = xml.EscapeText(&b, []byte(mac))
	b.WriteString("'/><source network='")
	_ = xml.EscapeText(&b, []byte(network))
	b.WriteString("'/><model type='")
	_ = xml.EscapeText(&b, []byte(model))
	b.WriteString("'/></interface>")
	return c.deviceOp(name, b.String(), live, true)
}

// DetachInterface retire la carte de MAC donnée (définition, et VM en marche si live).
func (c *Conn) DetachInterface(name, mac string, live bool) error {
	ifs, err := c.Interfaces(name)
	if err != nil {
		return err
	}
	for _, i := range ifs {
		if strings.EqualFold(i.MAC, mac) {
			x := fmt.Sprintf("<interface type='%s'><mac address='%s'/><source network='%s'/></interface>", i.Type, i.MAC, i.Network)
			return c.deviceOp(name, x, live, false)
		}
	}
	return nil
}

func (c *Conn) deviceOp(name, x string, live, attach bool) error {
	return c.do(func(lv *libvirt.Libvirt) error {
		d, err := c.lookup(lv, name)
		if err != nil {
			return err
		}
		flags := libvirt.DomainDeviceModifyConfig
		if live {
			flags |= libvirt.DomainDeviceModifyLive
		}
		if attach {
			return lv.DomainAttachDeviceFlags(d, x, uint32(flags))
		}
		return lv.DomainDetachDeviceFlags(d, x, uint32(flags))
	})
}

// SendKey envoie une touche (code Linux, ex. 28 = Entrée) à la VM.
func (c *Conn) SendKey(name string, code uint32) error {
	return c.do(func(lv *libvirt.Libvirt) error {
		d, err := lv.DomainLookupByName(name)
		if err != nil {
			return err
		}
		return lv.DomainSendKey(d, 0, 100, []uint32{code}, 0)
	})
}

var reCDROM = regexp.MustCompile(`(?s)\s*<disk type='file' device='cdrom'>.*?</disk>`)

// RemoveCDROMs retire de la définition (VM arrêtée) les lecteurs de CD et le démarrage sur CD.
func (c *Conn) RemoveCDROMs(name string) error {
	x, err := c.DomainXML(name, true)
	if err != nil {
		return err
	}
	x = reCDROM.ReplaceAllString(x, "")
	x = regexp.MustCompile(`\s*<boot dev='cdrom'/>`).ReplaceAllString(x, "")
	return c.DefineDomain(x)
}
