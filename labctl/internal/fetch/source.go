package fetch

import (
	"bufio"
	"bytes"
	"fmt"
	"path"
	"regexp"
	"strings"

	"labs/labctl/internal/module"
)

var reSumLine = regexp.MustCompile(`^([0-9a-fA-F]{32,128})\s+\*?(\S+)$`)

// Source renvoie, pour un disque fourni par un éditeur (vms.<vm>.source), l'adresse du fichier à télécharger,
// l'algorithme et l'empreinte attendue.
func Source(s *module.Source) (url, algo, want string, err error) {
	if s.URL != "" {
		algo, want, _ = strings.Cut(strings.ToLower(s.Checksum), ":")
		return s.URL, algo, want, nil
	}
	sums, err := Small(s.Sums)
	if err != nil {
		return "", "", "", fmt.Errorf("empreintes : %w", err)
	}
	// Un fichier d'empreintes peut en donner plusieurs pour le même fichier (md5 puis sha256… chez Parrot) :
	// la plus forte est retenue
	sc := bufio.NewScanner(bytes.NewReader(sums))
	name := ""
	for sc.Scan() {
		m := reSumLine.FindStringSubmatch(strings.TrimSpace(sc.Text()))
		if m == nil {
			continue
		}
		if ok, _ := path.Match(s.File, m[2]); ok && (name == "" || name == m[2]) && len(m[1]) > len(want) {
			name, want = m[2], strings.ToLower(m[1])
			algo = map[int]string{32: "md5", 40: "sha1", 64: "sha256", 128: "sha512"}[len(m[1])]
		}
	}
	if name == "" {
		return "", "", "", fmt.Errorf("aucun fichier %q dans %s", s.File, s.Sums)
	}
	return strings.TrimSuffix(s.Sums, path.Base(s.Sums)) + name, algo, want, nil
}
