package module

import (
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// SourcePath renvoie le chemin sur l'hôte d'un fichier « files: src » du module. src doit rester DANS le dossier du
// module : chemin relatif (ex. files/x.conf), sans « .. », sans « ~ », sans lien symbolique sous ce dossier. Un
// module.yaml — qui peut venir d'ailleurs — ne doit jamais pouvoir copier dans une VM un fichier de l'hôte (clé de
// pki/, ~/.ssh…). Le dossier du module lui-même peut être un lien (module développé ailleurs).
func SourcePath(dir, src string) (string, error) {
	s := strings.ReplaceAll(strings.TrimSpace(src), "\\", "/")
	if s == "" || strings.ContainsRune(s, 0) || strings.HasPrefix(s, "/") || strings.HasPrefix(s, "~") ||
		filepath.IsAbs(s) || reWinPath.MatchString(s) {
		return "", fmt.Errorf("src doit être un chemin relatif au dossier du module (ex. files/x.conf) : %q", src)
	}
	c := path.Clean(s)
	if c == "." || c == ".." || strings.HasPrefix(c, "../") {
		return "", fmt.Errorf("src sort du dossier du module : %q", src)
	}
	p := dir
	for _, part := range strings.Split(c, "/") {
		p = filepath.Join(p, part)
		fi, err := os.Lstat(p)
		if errors.Is(err, fs.ErrNotExist) {
			break // fichier absent : signalé ailleurs (validation, lecture)
		}
		if err != nil {
			return "", err
		}
		if fi.Mode()&fs.ModeSymlink != 0 {
			return "", fmt.Errorf("src : lien symbolique refusé dans le module (%s)", c)
		}
	}
	return filepath.Join(dir, filepath.FromSlash(c)), nil
}

// microsoftHosts : domaines dont labctl ne télécharge jamais rien (ISO Windows : téléchargement soumis à
// inscription et à conditions, à faire soi-même ; l'ISO est donné en chemin local dans windows: iso).
var microsoftHosts = []string{"microsoft.com", "windows.com", "windowsupdate.com", "msft.net", "live.com"}

// ForbiddenSource refuse ce que labctl ne télécharge jamais pour un disque fourni (source:) : rien depuis
// Microsoft, et aucun ISO (un ISO n'est pas un disque de VM ; l'installation de Windows passe par windows: iso,
// un fichier local). Vérifié à la validation et juste avant le téléchargement.
func ForbiddenSource(s *Source) error {
	if s == nil {
		return nil
	}
	for _, raw := range []string{s.URL, s.Sums} {
		if raw == "" {
			continue
		}
		u, err := url.Parse(raw)
		if err != nil {
			return fmt.Errorf("adresse invalide : %q", raw)
		}
		host := strings.ToLower(u.Hostname())
		for _, h := range microsoftHosts {
			if host == h || strings.HasSuffix(host, "."+h) {
				return fmt.Errorf("labctl ne télécharge jamais rien depuis Microsoft (%s) : pour Windows, windows: iso avec un ISO local", host)
			}
		}
		if strings.HasSuffix(strings.ToLower(u.Path), ".iso") {
			return fmt.Errorf("labctl ne télécharge jamais d'ISO (%s) : pour Windows, windows: iso avec un ISO local", path.Base(u.Path))
		}
	}
	for _, name := range []string{s.File, s.Extract} {
		if strings.HasSuffix(strings.ToLower(name), ".iso") {
			return fmt.Errorf("labctl ne télécharge jamais d'ISO (%s) : pour Windows, windows: iso avec un ISO local", name)
		}
	}
	return nil
}
