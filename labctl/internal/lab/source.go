package lab

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"

	"labs/labctl/internal/fetch"
	"labs/labctl/internal/module"
)

// Disques fournis par un éditeur (vms.<vm>.source d'un module.yaml) : téléchargés, vérifiés, extraits puis
// convertis en qcow2 dans <images>/sources/<module>-<vm>.qcow2. Ce fichier sert de base (en lecture seule)
// au disque de la VM : la recréer ne retélécharge rien.

// SourceDisk renvoie le chemin du disque qcow2 préparé pour une VM de module à source fournie.
func (l *Lab) SourceDisk(mod, vm string) string {
	return filepath.Join(l.ImagesDir(), "sources", mod+"-"+vm+".qcow2")
}

// EnsureSource prépare le disque d'une VM à source fournie (rien à faire s'il existe déjà).
func (l *Lab) EnsureSource(mod, vm string, s *module.Source, out io.Writer) (string, error) {
	if err := module.ForbiddenSource(s); err != nil { // même règle qu'à la validation, juste avant tout téléchargement
		return "", err
	}
	dst := l.SourceDisk(mod, vm)
	if _, err := os.Stat(dst); err == nil {
		return dst, nil
	}
	cache := filepath.Join(l.ImagesDir(), "sources", "cache")
	if err := os.MkdirAll(cache, 0o755); err != nil {
		return "", err
	}
	url, algo, want, err := fetch.Source(s)
	if err != nil {
		return "", err
	}
	name := path.Base(url)
	if name == "download" { // SourceForge : …/fichier.zip/download
		name = path.Base(path.Dir(url))
	}
	file := filepath.Join(cache, name)

	// Fichier déjà en cache et intact : pas de nouveau téléchargement
	if got, err := fetch.FileHash(file, algo); err != nil || got != want {
		fmt.Fprintf(out, "==> Téléchargement de %s\n", name)
		if err := fetch.Download(url, file+".part", out); err != nil {
			return "", err
		}
		fmt.Fprintf(out, "==> Vérification %s\n", algo)
		if got, err := fetch.FileHash(file+".part", algo); err != nil || got != want {
			os.Remove(file + ".part")
			return "", fmt.Errorf("empreinte %s invalide pour %s (fichier supprimé, relancer)", algo, name)
		}
		if err := os.Rename(file+".part", file); err != nil {
			return "", err
		}
	}
	fmt.Fprintf(out, "ok  %s (empreinte %s conforme)\n", name, algo)

	disk := file
	if s.Extract != "" {
		dir, err := os.MkdirTemp(cache, "extract-")
		if err != nil {
			return "", err
		}
		defer os.RemoveAll(dir)
		fmt.Fprintf(out, "==> Extraction de %s\n", s.Extract)
		var cmd *exec.Cmd
		if _, err := exec.LookPath("7z"); err == nil {
			cmd = exec.Command("7z", "e", "-y", "-bd", "-r", "-o"+dir, "--", file, s.Extract)
		} else if strings.HasSuffix(name, ".zip") {
			cmd = exec.Command("unzip", "-q", "-j", "-o", file, s.Extract, "-d", dir)
		} else {
			return "", fmt.Errorf("commande manquante : 7z (paquet p7zip-full)")
		}
		if b, err := cmd.CombinedOutput(); err != nil {
			return "", fmt.Errorf("extraction : %s", strings.TrimSpace(string(b)))
		}
		// Rien d'autre que des fichiers ordinaires : une archive peut contenir des liens symboliques vers l'hôte
		if err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			return fetch.RegularFile(p)
		}); err != nil {
			return "", fmt.Errorf("archive %s refusée : %w", name, err)
		}
		matches, _ := filepath.Glob(filepath.Join(dir, s.Extract))
		if len(matches) == 0 {
			return "", fmt.Errorf("pas de fichier %q dans %s", s.Extract, name)
		}
		disk = matches[0]
	}
	// Structure vérifiée avant conversion (aucune référence vers un fichier de l'hôte), format explicite ensuite
	format, err := fetch.CheckDisk(disk)
	if err != nil {
		return "", err
	}
	fmt.Fprintf(out, "==> Conversion en qcow2 (depuis %s)\n", format)
	if b, err := exec.Command("qemu-img", "convert", "-f", format, "-O", "qcow2", disk, dst+".part").CombinedOutput(); err != nil {
		os.Remove(dst + ".part")
		return "", fmt.Errorf("qemu-img convert : %s", strings.TrimSpace(string(b)))
	}
	if err := os.Chmod(dst+".part", 0o644); err != nil {
		return "", err
	}
	if err := os.Rename(dst+".part", dst); err != nil {
		return "", err
	}
	fmt.Fprintf(out, "ok  disque prêt : %s\n", dst)
	return dst, nil
}
