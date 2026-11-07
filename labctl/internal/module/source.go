package module

import (
	"errors"
	"fmt"
	"io/fs"
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
