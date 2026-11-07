package lab

import (
	"bufio"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Image est une image cloud (cloud-init) du catalogue.
type Image struct {
	Name   string
	URL    string
	Sums   string // fichier d'empreintes publié par la distribution
	Algo   string // sha256 ou sha512
	OSInfo string // identifiant libosinfo (métadonnée de la VM)
}

// Images : catalogue des images utilisables par « labctl vm create -i ». Ajouter une entrée = une URL,
// son fichier d'empreintes et un identifiant libosinfo.
var Images = map[string]Image{
	"debian12": {URL: "https://cloud.debian.org/images/cloud/bookworm/latest/debian-12-generic-amd64.qcow2",
		Sums: "https://cloud.debian.org/images/cloud/bookworm/latest/SHA512SUMS", Algo: "sha512", OSInfo: "http://debian.org/debian/12"},
	"debian13": {URL: "https://cloud.debian.org/images/cloud/trixie/latest/debian-13-generic-amd64.qcow2",
		Sums: "https://cloud.debian.org/images/cloud/trixie/latest/SHA512SUMS", Algo: "sha512", OSInfo: "http://debian.org/debian/13"},
	"ubuntu2404": {URL: "https://cloud-images.ubuntu.com/noble/current/noble-server-cloudimg-amd64.img",
		Sums: "https://cloud-images.ubuntu.com/noble/current/SHA256SUMS", Algo: "sha256", OSInfo: "http://ubuntu.com/ubuntu/24.04"},
	"ubuntu2604": {URL: "https://cloud-images.ubuntu.com/resolute/current/resolute-server-cloudimg-amd64.img",
		Sums: "https://cloud-images.ubuntu.com/resolute/current/SHA256SUMS", Algo: "sha256", OSInfo: "http://ubuntu.com/ubuntu/24.04"},
	"rocky9": {URL: "https://dl.rockylinux.org/pub/rocky/9/images/x86_64/Rocky-9-GenericCloud-Base.latest.x86_64.qcow2",
		Sums: "https://dl.rockylinux.org/pub/rocky/9/images/x86_64/CHECKSUM", Algo: "sha256", OSInfo: "http://rockylinux.org/rocky/9"},
	"alma9": {URL: "https://repo.almalinux.org/almalinux/9/cloud/x86_64/images/AlmaLinux-9-GenericCloud-latest.x86_64.qcow2",
		Sums: "https://repo.almalinux.org/almalinux/9/cloud/x86_64/images/CHECKSUM", Algo: "sha256", OSInfo: "http://almalinux.org/almalinux/9"},
}

// ImageNames renvoie les noms du catalogue, triés.
func ImageNames() []string {
	out := make([]string, 0, len(Images))
	for n := range Images {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// ImagesDir renvoie le dossier des images de base.
func (l *Lab) ImagesDir() string {
	return l.Setting("LABS_IMAGES_DIR", filepath.Join(l.Setting("LABS_STORAGE_DIR", ""), "images"))
}

// ImagePath renvoie le chemin local d'une image du catalogue.
func (l *Lab) ImagePath(name string) string { return filepath.Join(l.ImagesDir(), name+".qcow2") }

var (
	reSumGNU = regexp.MustCompile(`^([0-9a-fA-F]{64,128})\s+\*?(\S+)$`)
	reSumBSD = regexp.MustCompile(`^SHA(256|512) \((\S+)\) = ([0-9a-fA-F]{64,128})$`)
)

// ParseSums cherche l'empreinte d'un fichier dans un fichier d'empreintes (formats GNU et BSD).
func ParseSums(r io.Reader, file string) (string, error) {
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if m := reSumGNU.FindStringSubmatch(line); m != nil && m[2] == file {
			return strings.ToLower(m[1]), nil
		}
		if m := reSumBSD.FindStringSubmatch(line); m != nil && m[2] == file {
			return strings.ToLower(m[3]), nil
		}
	}
	if err := sc.Err(); err != nil {
		return "", err
	}
	return "", fmt.Errorf("pas d'empreinte pour %s", file)
}

var httpClient = &http.Client{Timeout: 0} // téléchargements longs : pas de délai global

// EnsureImage télécharge une image du catalogue si elle est absente (ou si force), vérifie son empreinte
// et sa structure (qemu-img), puis la publie de façon atomique. Progression écrite sur out.
func (l *Lab) EnsureImage(name string, force bool, out io.Writer) (string, error) {
	img, ok := Images[name]
	if !ok {
		return "", fmt.Errorf("image inconnue : %s (%s)", name, strings.Join(ImageNames(), ", "))
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
	file := path.Base(img.URL)

	// Empreinte attendue, avant le téléchargement
	resp, err := httpClient.Get(img.Sums)
	if err != nil {
		return "", fmt.Errorf("empreintes de %s : %w", name, err)
	}
	want, err := ParseSums(resp.Body, file)
	resp.Body.Close()
	if err != nil {
		return "", fmt.Errorf("%s : %w", img.Sums, err)
	}

	part := dst + ".part"
	if force {
		os.Remove(part)
	}
	fmt.Fprintf(out, "==> Téléchargement de %s\n", name)
	if err := download(img.URL, part, out); err != nil {
		return "", err
	}
	fmt.Fprintf(out, "==> Vérification %s\n", img.Algo)
	var h hash.Hash = sha256.New()
	if img.Algo == "sha512" {
		h = sha512.New()
	}
	f, err := os.Open(part)
	if err != nil {
		return "", err
	}
	_, err = io.Copy(h, f)
	f.Close()
	if err != nil {
		return "", err
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != want {
		os.Remove(part)
		return "", fmt.Errorf("empreinte %s invalide pour %s (fichier supprimé, relancer)", img.Algo, name)
	}
	if _, err := checkDisk(part); err != nil { // structure : aucune référence vers un fichier de l'hôte
		os.Remove(part)
		return "", err
	}
	if err := os.Chmod(part, 0o644); err != nil {
		return "", err
	}
	if err := os.Rename(part, dst); err != nil {
		return "", err
	}
	fmt.Fprintf(out, "ok  %s -> %s (empreinte %s conforme)\n", name, dst, img.Algo)
	return dst, nil
}

// download télécharge url dans dst, en reprenant un fichier partiel existant.
func download(url, dst string, out io.Writer) error {
	f, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	offset, _ := f.Seek(0, io.SeekEnd)
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	if offset > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", offset))
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		if offset > 0 { // reprise refusée par le serveur : on repart de zéro
			if err := f.Truncate(0); err != nil {
				return err
			}
			if _, err := f.Seek(0, io.SeekStart); err != nil {
				return err
			}
			offset = 0
		}
	case http.StatusPartialContent:
	case http.StatusRequestedRangeNotSatisfiable:
		return nil // déjà complet
	default:
		return fmt.Errorf("téléchargement de %s : %s", url, resp.Status)
	}
	total := offset + resp.ContentLength
	pw := &progress{out: out, done: offset, total: total, last: time.Now()}
	if _, err := io.Copy(io.MultiWriter(f, pw), resp.Body); err != nil {
		return fmt.Errorf("téléchargement interrompu (relancer pour reprendre) : %w", err)
	}
	pw.finish()
	return nil
}

type progress struct {
	out         io.Writer
	done, total int64
	last        time.Time
}

func (p *progress) Write(b []byte) (int, error) {
	p.done += int64(len(b))
	if time.Since(p.last) > 2*time.Second && p.total > 0 {
		fmt.Fprintf(p.out, "    %3d %%  (%d / %d Mo)\n", p.done*100/p.total, p.done>>20, p.total>>20)
		p.last = time.Now()
	}
	return len(b), nil
}

func (p *progress) finish() {
	if p.total > 0 {
		fmt.Fprintf(p.out, "    100 %%  (%d Mo)\n", p.done>>20)
	}
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
