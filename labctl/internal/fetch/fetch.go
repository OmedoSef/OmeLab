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
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"labs/labctl/internal/module"
)

// client : pas de délai global (téléchargements de plusieurs Gio), mais des délais de connexion et d'attente de la
// réponse. Chaque redirection est contrôlée : https seulement (l'empreinte attendue ne doit pas pouvoir être
// réécrite en chemin), jamais vers Microsoft, jamais vers un ISO.
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
	if err := module.MicrosoftHost(req.URL.Hostname()); err != nil {
		return err
	}
	// Une adresse qui n'était pas un ISO ne peut pas mener à un ISO (seuls les ISO voulus et à empreinte figée,
	// comme les pilotes virtio-win, sont téléchargés tels quels)
	if module.ForbiddenName(via[0].URL.Path) == nil {
		return module.ForbiddenName(req.URL.Path)
	}
	return nil
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

// parseSums cherche l'empreinte d'un fichier dans un fichier d'empreintes (formats GNU et BSD).
func parseSums(r io.Reader, file string) (string, error) {
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

// newHash renvoie la fonction de hachage nommée : md5, sha1, sha256 ou sha512. Un autre nom est une erreur (jamais
// un repli silencieux sur sha256, qui ferait échouer — ou passer — une vérification pour une mauvaise raison).
func newHash(algo string) (hash.Hash, error) {
	switch algo {
	case "md5":
		return md5.New(), nil
	case "sha1":
		return sha1.New(), nil
	case "sha256":
		return sha256.New(), nil
	case "sha512":
		return sha512.New(), nil
	}
	return nil, fmt.Errorf("algorithme d'empreinte inconnu : %q", algo)
}

// FileHash renvoie l'empreinte (hexadécimale) d'un fichier.
func FileHash(file, algo string) (string, error) {
	h, err := newHash(algo)
	if err != nil {
		return "", err
	}
	f, err := os.Open(file)
	if err != nil {
		return "", err
	}
	defer f.Close()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// verified télécharge url vers dst, sans jamais publier un fichier non vérifié : téléchargement dans dst.part
// (repris s'il existe), empreinte comparée à want, contrôle check facultatif (structure d'un disque…), puis
// renommage atomique. Un fichier partiel qui échoue à la vérification est supprimé.
func verified(url, dst, algo, want string, check func(string) error, out io.Writer) error {
	part := dst + ".part"
	if err := download(url, part, out); err != nil {
		return err
	}
	fmt.Fprintf(out, "==> Vérification %s\n", algo)
	got, err := FileHash(part, algo)
	if err != nil {
		return err
	}
	if got != strings.ToLower(want) {
		os.Remove(part)
		return fmt.Errorf("empreinte %s invalide pour %s (fichier supprimé, relancer)", algo, filepath.Base(dst))
	}
	if check != nil {
		if err := check(part); err != nil {
			os.Remove(part)
			return err
		}
	}
	return os.Rename(part, dst)
}

// Cached renvoie dst s'il est déjà là et intact (empreinte want), sinon le télécharge et le vérifie (verified).
// label nomme le fichier dans les messages.
func Cached(url, dst, algo, want, label string, out io.Writer) error {
	if got, err := FileHash(dst, algo); err == nil && got == strings.ToLower(want) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	fmt.Fprintf(out, "==> Téléchargement de %s\n", label)
	return verified(url, dst, algo, want, nil, out)
}

// download télécharge url dans dst, en reprenant un fichier partiel existant.
func download(url, dst string, out io.Writer) error {
	f, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	offset, _ := f.Seek(0, io.SeekEnd)
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return err
	}
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
