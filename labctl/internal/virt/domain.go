package virt

import (
	"encoding/xml"
	"fmt"
	"path/filepath"
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
