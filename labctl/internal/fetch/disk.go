package fetch

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
)

// Contrôle d'une image disque venue de l'extérieur (image cloud téléchargée, disque fourni par un éditeur) avant
// de l'utiliser. Un format comme qcow2 ou vmdk peut désigner d'autres fichiers : fichier de base (backing file),
// fichier de données externe, extents VMDK. qemu-img les suit, si bien qu'une image piégée ferait lire à la VM
// — ou recopier par la conversion — un fichier de l'hôte (clé SSH, clé de la PKI…). L'empreinte attendue ne
// protège pas d'un éditeur ou d'un auteur de module malveillant.

// diskFormats : formats de disque acceptés (pas de pseudo-format réseau ou chiffré).
var diskFormats = []string{"raw", "qcow2", "vmdk", "vdi", "vhdx", "vpc", "qed"}

// qemuInfo : champs utiles de « qemu-img info --output=json ».
type qemuInfo struct {
	Format              string `json:"format"`
	BackingFilename     string `json:"backing-filename"`
	FullBackingFilename string `json:"full-backing-filename"`
	FormatSpecific      struct {
		Type string `json:"type"`
		Data struct {
			DataFile string `json:"data-file"`
			Extents  []struct {
				Filename string `json:"filename"`
			} `json:"extents"`
		} `json:"data"`
	} `json:"format-specific"`
}

// checkDiskInfo vérifie la description qemu-img d'une image (path) et renvoie son format.
func checkDiskInfo(path string, info []byte) (string, error) {
	var q qemuInfo
	if err := json.Unmarshal(info, &q); err != nil {
		return "", fmt.Errorf("description de l'image illisible : %w", err)
	}
	switch {
	case !slices.Contains(diskFormats, q.Format):
		return "", fmt.Errorf("format d'image refusé : %q (acceptés : %s)", q.Format, strings.Join(diskFormats, ", "))
	case q.BackingFilename != "" || q.FullBackingFilename != "":
		return "", fmt.Errorf("image refusée : elle dépend d'un autre fichier (backing file %q)", q.BackingFilename)
	case q.FormatSpecific.Data.DataFile != "":
		return "", fmt.Errorf("image refusée : données dans un fichier externe (%q)", q.FormatSpecific.Data.DataFile)
	}
	for _, e := range q.FormatSpecific.Data.Extents { // vmdk : extents, seulement à côté de l'image
		if e.Filename != "" && filepath.Dir(filepath.Clean(e.Filename)) != filepath.Dir(filepath.Clean(path)) {
			return "", fmt.Errorf("image refusée : extent hors de son dossier (%q)", e.Filename)
		}
	}
	return q.Format, nil
}

// CheckDisk décrit l'image avec qemu-img et la vérifie ; renvoie son format, à passer explicitement (-f) ensuite.
func CheckDisk(path string) (string, error) {
	out, err := exec.Command("qemu-img", "info", "-U", "--output=json", path).Output()
	if err != nil {
		return "", fmt.Errorf("image invalide (%s) : %v", filepath.Base(path), err)
	}
	format, err := checkDiskInfo(path, out)
	if err != nil {
		return "", err
	}
	// L'image et chacun de ses morceaux (extents VMDK) doivent être des fichiers ordinaires : un lien symbolique
	// posé à côté de l'image (archive d'un éditeur) ferait lire à qemu-img un fichier de l'hôte
	files := []string{path}
	var q qemuInfo
	_ = json.Unmarshal(out, &q) // déjà lu sans erreur par checkDiskInfo
	for _, e := range q.FormatSpecific.Data.Extents {
		if e.Filename == "" {
			continue
		}
		f := e.Filename
		if !filepath.IsAbs(f) {
			f = filepath.Join(filepath.Dir(path), f)
		}
		files = append(files, f)
	}
	for _, f := range files {
		if err := RegularFile(f); err != nil {
			return "", fmt.Errorf("image refusée : %w", err)
		}
	}
	return format, nil
}

// RegularFile vérifie que path est un fichier ordinaire, sans suivre de lien symbolique.
func RegularFile(path string) error {
	fi, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("%s n'est pas un fichier ordinaire (%s)", filepath.Base(path), fi.Mode().Type())
	}
	return nil
}
