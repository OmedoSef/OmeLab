// Package fetch fait entrer dans le lab ce qui vient de l'extérieur : images cloud du catalogue, disques fournis
// par un éditeur, outils téléchargés (mitmproxy, pilotes et OpenSSH pour Windows). Tout y est vérifié avant usage :
// empreinte attendue, signature de l'éditeur quand il en publie une (sig.go), structure des disques (disk.go).
// C'est la frontière de confiance vis-à-vis d'Internet : un audit de la chaîne d'approvisionnement commence ici.
package fetch

import (
	"bufio"
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"net"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	"labs/labctl/internal/module"
)

// client : pas de délai global (téléchargements de plusieurs Gio), mais des délais de connexion et d'attente de la
// réponse. Chaque redirection est contrôlée : https seulement (l'empreinte attendue ne doit pas pouvoir être
// réécrite en chemin), et jamais vers Microsoft.
var client = &http.Client{Transport: transport(), CheckRedirect: checkRedirect}

func transport() *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.DialContext = (&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}).DialContext
	t.TLSHandshakeTimeout = 30 * time.Second
	t.ResponseHeaderTimeout = time.Minute
	return t
}

func checkRedirect(req *http.Request, via []*http.Request) error {
	switch {
	case len(via) >= 10:
		return errors.New("trop de redirections")
	case req.URL.Scheme != "https":
		return fmt.Errorf("redirection refusée vers %s : https attendu", req.URL.Redacted())
	}
	return module.MicrosoftHost(req.URL.Hostname())
}

// Small télécharge un petit fichier (empreintes, signature : 4 Mo au plus) en mémoire.
func Small(url string) ([]byte, error) {
	resp, err := client.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s : %s", url, resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 4<<20))
}

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

// NewHash renvoie la fonction de hachage nommée (md5, sha1, sha256, sha512 ; sha256 par défaut).
func NewHash(algo string) hash.Hash {
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

// FileHash renvoie l'empreinte (hexadécimale) d'un fichier.
func FileHash(file, algo string) (string, error) {
	f, err := os.Open(file)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := NewHash(algo)
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Download télécharge url dans dst, en reprenant un fichier partiel existant.
func Download(url, dst string, out io.Writer) error {
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
	resp, err := client.Do(req)
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
