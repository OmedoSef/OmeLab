package lab

import (
	"bufio"
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"fmt"
	"hash"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"labs/labctl/internal/module"
)

// Disques fournis par un éditeur (vms.<vm>.source d'un module.yaml) : téléchargés, vérifiés, extraits puis
// convertis en qcow2 dans <images>/sources/<module>-<vm>.qcow2. Ce fichier sert de base (en lecture seule)
// au disque de la VM : la recréer ne retélécharge rien.

var reSumLine = regexp.MustCompile(`^([0-9a-fA-F]{32,128})\s+\*?(\S+)$`)

// resolveSource renvoie l'adresse du fichier à télécharger, l'algorithme et l'empreinte attendue.
func resolveSource(s *module.Source) (url, algo, want string, err error) {
	if s.URL != "" {
		algo, want, _ = strings.Cut(strings.ToLower(s.Checksum), ":")
		return s.URL, algo, want, nil
	}
	resp, err := httpClient.Get(s.Sums)
	if err != nil {
		return "", "", "", fmt.Errorf("empreintes : %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", "", "", fmt.Errorf("empreintes %s : %s", s.Sums, resp.Status)
	}
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		m := reSumLine.FindStringSubmatch(strings.TrimSpace(sc.Text()))
		if m == nil {
			continue
		}
		if ok, _ := path.Match(s.File, m[2]); ok {
			algo = map[int]string{32: "md5", 40: "sha1", 64: "sha256", 128: "sha512"}[len(m[1])]
			return strings.TrimSuffix(s.Sums, path.Base(s.Sums)) + m[2], algo, strings.ToLower(m[1]), nil
		}
	}
	return "", "", "", fmt.Errorf("aucun fichier %q dans %s", s.File, s.Sums)
}

func newHash(algo string) hash.Hash {
	switch algo {
	case "md5":
		return md5.New()
	case "sha1":
		return sha1.New()
	case "sha512":
		return sha512.New()
	}
	return sha256.New()
}

func fileHash(file, algo string) (string, error) {
	f, err := os.Open(file)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := newHash(algo)
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// SourceDisk renvoie le chemin du disque qcow2 préparé pour une VM de module à source fournie.
func (l *Lab) SourceDisk(mod, vm string) string {
	return filepath.Join(l.ImagesDir(), "sources", mod+"-"+vm+".qcow2")
}

// EnsureSource prépare le disque d'une VM à source fournie (rien à faire s'il existe déjà).
func (l *Lab) EnsureSource(mod, vm string, s *module.Source, out io.Writer) (string, error) {
	dst := l.SourceDisk(mod, vm)
	if _, err := os.Stat(dst); err == nil {
		return dst, nil
	}
	cache := filepath.Join(l.ImagesDir(), "sources", "cache")
	if err := os.MkdirAll(cache, 0o755); err != nil {
		return "", err
	}
	url, algo, want, err := resolveSource(s)
	if err != nil {
		return "", err
	}
	name := path.Base(url)
	if name == "download" { // SourceForge : …/fichier.zip/download
		name = path.Base(path.Dir(url))
	}
	file := filepath.Join(cache, name)

	// Fichier déjà en cache et intact : pas de nouveau téléchargement
	if got, err := fileHash(file, algo); err != nil || got != want {
		fmt.Fprintf(out, "==> Téléchargement de %s\n", name)
		if err := download(url, file+".part", out); err != nil {
			return "", err
		}
		fmt.Fprintf(out, "==> Vérification %s\n", algo)
		if got, err := fileHash(file+".part", algo); err != nil || got != want {
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
			cmd = exec.Command("7z", "e", "-y", "-bd", "-o"+dir, file, s.Extract, "-r")
		} else if strings.HasSuffix(name, ".zip") {
			cmd = exec.Command("unzip", "-q", "-j", "-o", file, s.Extract, "-d", dir)
		} else {
			return "", fmt.Errorf("commande manquante : 7z (paquet p7zip-full)")
		}
		if b, err := cmd.CombinedOutput(); err != nil {
			return "", fmt.Errorf("extraction : %s", strings.TrimSpace(string(b)))
		}
		matches, _ := filepath.Glob(filepath.Join(dir, s.Extract))
		if len(matches) == 0 {
			return "", fmt.Errorf("pas de fichier %q dans %s", s.Extract, name)
		}
		disk = matches[0]
	}
	fmt.Fprintf(out, "==> Conversion en qcow2\n")
	if b, err := exec.Command("qemu-img", "convert", "-O", "qcow2", disk, dst+".part").CombinedOutput(); err != nil {
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
