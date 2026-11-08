package lab

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"labs/labctl/internal/fetch"
	"labs/labctl/internal/virt"
)

// Remise à zéro du lab (labctl clear) : VMs et réseaux de tous les modules, puis la passerelle. En option, la PKI
// (--pki) et ce que labctl a téléchargé (--images). Restent toujours : les module.yaml et files/, la configuration
// (config.env, lab.yaml, *.local.*), les VMs créées à la main hors des modules, et les fichiers déposés par
// l'utilisateur dans le dossier des images (ISO Windows…).

// ClearOptions : ce que Clear supprime en plus des modules et de la passerelle.
type ClearOptions struct {
	PKI    bool // la PKI entière (CA racine, intermédiaire, CA des modules, certificats)
	Images bool // images du catalogue, disques éditeurs convertis, cache des téléchargements
}

// ClearPlan décrit ce que Clear supprimera, à montrer avant de demander confirmation.
type ClearPlan struct {
	Modules []string // modules qui ont un réseau ou des VMs
	VMs     []string // VMs de ces modules (noms libvirt)
	Gateway bool     // la passerelle existe
	PKIDir  string   // dossier de la PKI à supprimer ("" : rien)
	Files   []string // téléchargements à supprimer (--images)
	Kept    []string // téléchargements gardés : base d'une VM qui reste (créée à la main)
	Bytes   int64    // taille des fichiers et dossiers de Files
}

// Empty indique qu'il n'y a rien à supprimer.
func (p *ClearPlan) Empty() bool {
	return len(p.Modules) == 0 && !p.Gateway && p.PKIDir == "" && len(p.Files) == 0
}

// PlanClear calcule ce que Clear supprimera, sans rien toucher.
func (l *Lab) PlanClear(o ClearOptions) (*ClearPlan, error) {
	mods, err := l.Modules()
	if err != nil {
		return nil, err
	}
	p := &ClearPlan{}
	v := l.Hypervisor()
	for _, m := range mods {
		if m.YAML == nil {
			continue
		}
		var vms []string
		for _, vm := range m.YAML.VMNames() {
			if lv := m.LibvirtName(vm); l.DomState(lv) != virt.StateAbsent {
				vms = append(vms, lv)
			}
		}
		if len(vms) > 0 || v.NetExists(m.YAML.NetworkName()) {
			p.Modules = append(p.Modules, m.Name)
			p.VMs = append(p.VMs, vms...)
		}
	}
	p.Gateway = l.DomState(GatewayVM) != virt.StateAbsent
	if o.PKI {
		if dir := l.PKIDir(); isPKIDir(dir) {
			p.PKIDir = dir
		}
	}
	if o.Images {
		removed := p.VMs
		if p.Gateway {
			removed = append(slices.Clone(removed), GatewayVM) // supprimée elle aussi : ne retient pas son image de base
		}
		p.Files, p.Kept = l.downloads(mods, removed)
		for _, f := range p.Files {
			p.Bytes += diskUsage(f)
		}
	}
	return p, nil
}

// isPKIDir : le dossier est bien une PKI de labctl (racine ou intermédiaire présente), jamais un autre dossier que
// LABS_PKI_DIR désignerait par erreur.
func isPKIDir(dir string) bool {
	for _, sub := range []string{"root", "intermediate"} {
		if fi, err := os.Lstat(filepath.Join(dir, sub)); err == nil && fi.IsDir() {
			return true
		}
	}
	return false
}

// downloads liste ce que labctl a téléchargé (images du catalogue, disques éditeurs convertis des modules, cache) ;
// une image qui sert de base à une VM qui reste (hors des VMs supprimées) est gardée.
func (l *Lab) downloads(mods []*Module, removed []string) (files, kept []string) {
	gone := map[string]bool{}
	for _, vm := range removed {
		gone[vm] = true
	}
	stillUsed := func(path string) bool {
		var users []string
		for _, u := range l.ImageUsers(path) {
			if !gone[u] {
				users = append(users, u)
			}
		}
		if len(users) > 0 {
			kept = append(kept, fmt.Sprintf("%s (base de %s)", path, strings.Join(users, ", ")))
			return true
		}
		return false
	}
	add := func(path string) {
		for _, p := range []string{path, path + ".part"} {
			if _, err := os.Lstat(p); err == nil && !stillUsed(p) {
				files = append(files, p)
			}
		}
	}
	for _, name := range fetch.ImageNames() {
		add(l.ImagePath(name))
	}
	for _, m := range mods {
		if m.YAML == nil {
			continue
		}
		for _, vm := range m.YAML.VMNames() {
			if m.YAML.VMs[vm].Source != nil {
				add(l.SourceDisk(m.Name, vm))
			}
		}
	}
	if fi, err := os.Lstat(filepath.Join(l.ImagesDir(), "sources", "cache")); err == nil && fi.IsDir() {
		files = append(files, filepath.Join(l.ImagesDir(), "sources", "cache"))
	}
	return files, kept
}

// diskUsage : taille d'un fichier ou d'un dossier (sans suivre les liens).
func diskUsage(path string) int64 {
	var n int64
	_ = filepath.WalkDir(path, func(_ string, d fs.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() {
			if fi, err := d.Info(); err == nil {
				n += fi.Size()
			}
		}
		return nil
	})
	return n
}

// Clear remet le lab à zéro selon le plan (voir PlanClear) : chaque module est détruit (labctl <module> destroy),
// puis la passerelle. Si l'une de ces suppressions échoue, la PKI et les téléchargements ne sont pas touchés : des
// VMs restantes peuvent encore en dépendre.
func (l *Lab) Clear(p *ClearPlan, out io.Writer) error {
	var errs []error
	for _, m := range p.Modules {
		fmt.Fprintf(out, "==> Module %s\n", m)
		if err := l.ModuleDestroy(m, nil, out); err != nil {
			errs = append(errs, fmt.Errorf("module %s : %w", m, err))
		}
	}
	if p.Gateway {
		fmt.Fprintf(out, "==> Passerelle %s\n", GatewayVM)
		if err := l.Gateway(out).Destroy(); err != nil {
			errs = append(errs, fmt.Errorf("passerelle : %w", err))
		}
	}
	if len(errs) > 0 {
		if p.PKIDir != "" || len(p.Files) > 0 {
			fmt.Fprintln(out, "attention: PKI et téléchargements conservés, des VMs n'ont pas pu être supprimées")
		}
		return errors.Join(errs...)
	}
	if p.PKIDir != "" {
		if err := os.RemoveAll(p.PKIDir); err != nil {
			return fmt.Errorf("PKI : %w", err)
		}
		fmt.Fprintf(out, "ok  PKI supprimée (%s) ; « labctl pki init » en recrée une\n", p.PKIDir)
	}
	for _, f := range p.Files {
		if err := os.RemoveAll(f); err != nil {
			errs = append(errs, err)
			continue
		}
		fmt.Fprintf(out, "ok  supprimé : %s\n", f)
	}
	return errors.Join(errs...)
}
