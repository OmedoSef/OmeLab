package lab

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"labs/labctl/internal/fetch"
)

// ImagesDir renvoie le dossier des images de base.
func (l *Lab) ImagesDir() string {
	return l.Setting("LABS_IMAGES_DIR", filepath.Join(l.Setting("LABS_STORAGE_DIR", ""), "images"))
}

// ImagePath renvoie le chemin local d'une image du catalogue.
func (l *Lab) ImagePath(name string) string { return filepath.Join(l.ImagesDir(), name+".qcow2") }

// EnsureImage télécharge une image du catalogue si elle est absente (ou si force), vérifie son empreinte
// et sa structure (qemu-img), puis la publie de façon atomique. Progression écrite sur out.
func (l *Lab) EnsureImage(name string, force bool, out io.Writer) (string, error) {
	if _, ok := fetch.Images[name]; !ok {
		return "", fmt.Errorf("image inconnue : %s (%s)", name, strings.Join(fetch.ImageNames(), ", "))
	}
	dst := l.ImagePath(name)
	if _, err := os.Stat(dst); err == nil && !force {
		return dst, nil
	}
	if force {
		// Remplacer une image de base sous des disques liés les corromprait
		if users := l.ImageUsers(dst); len(users) > 0 {
			return "", fmt.Errorf("image %s utilisée comme base par : %s (supprimer ces VMs d'abord, ou garder l'image actuelle)", name, strings.Join(users, ", "))
		}
	}
	if err := os.MkdirAll(l.ImagesDir(), 0o755); err != nil {
		return "", err
	}
	if err := fetch.FetchImage(name, dst, force, out); err != nil {
		return "", err
	}
	return dst, nil
}

// ImageUsers renvoie les VMs (dossiers de VMs) dont le disque s'appuie sur l'image de base donnée.
func (l *Lab) ImageUsers(image string) []string {
	var users []string
	dirs, _ := os.ReadDir(l.VMDir())
	for _, d := range dirs {
		disk := filepath.Join(l.VMDir(), d.Name(), "disk.qcow2")
		if _, err := os.Stat(disk); err != nil {
			continue
		}
		out, err := exec.Command("qemu-img", "info", "-U", "--output=json", disk).Output()
		if err != nil {
			users = append(users, d.Name()+" (disque illisible : prudence)")
			continue
		}
		var info struct {
			Backing string `json:"full-backing-filename"`
		}
		if json.Unmarshal(out, &info) == nil && filepath.Clean(info.Backing) == filepath.Clean(image) {
			users = append(users, d.Name())
		}
	}
	return users
}
