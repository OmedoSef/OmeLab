package lab

import (
	"archive/tar"
	"bytes"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"labs/labctl/internal/coreconf"
)

// DiffLine est une ligne de différence : Op vaut " " (inchangée), "-" (retirée), "+" (ajoutée)
// ou "…" (lignes inchangées omises).
type DiffLine struct {
	Op   string `json:"op"`
	Text string `json:"text"`
}

// FileChange décrit un fichier de configuration qui changera sur la passerelle.
type FileChange struct {
	File  string     `json:"file"`
	Lines []DiffLine `json:"lines"`
}

// DeployedCoreConfig lit la configuration actuellement installée sur la passerelle (même découpage
// que coreconf.Files : gwdns/..., networkd/...).
func (l *Lab) DeployedCoreConfig() (coreconf.Files, error) {
	cmd := l.Command("core", "ssh", `sudo tar -C / -cf - etc/gwdns $(ls /etc/systemd/network/50-gw-*.network 2>/dev/null | sed 's|^/||') 2>/dev/null`)
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	out, err := cmd.Output()
	if err != nil && len(out) == 0 {
		return nil, fmt.Errorf("lecture de la configuration de la passerelle : %w", err)
	}
	files := coreconf.Files{}
	tr := tar.NewReader(bytes.NewReader(out))
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if h.Typeflag != tar.TypeReg {
			continue
		}
		b, err := io.ReadAll(tr)
		if err != nil {
			return nil, err
		}
		name := h.Name
		switch {
		case strings.HasPrefix(name, "etc/gwdns/"):
			name = "gwdns/" + strings.TrimPrefix(name, "etc/gwdns/")
		case strings.HasPrefix(name, "etc/systemd/network/"):
			name = "networkd/" + strings.TrimPrefix(name, "etc/systemd/network/")
		default:
			continue
		}
		files[name] = string(b)
	}
	return files, nil
}

// PendingCoreChanges compare la configuration générée (module.env, core.conf) à celle de la passerelle.
// Liste vide : rien à appliquer.
func (l *Lab) PendingCoreChanges() ([]FileChange, error) {
	in, err := l.CoreInput()
	if err != nil {
		return nil, err
	}
	want, err := coreconf.Render(in)
	if err != nil {
		return nil, err
	}
	have, err := l.DeployedCoreConfig()
	if err != nil {
		return nil, err
	}
	return DiffFiles(have, want), nil
}

// DiffFiles compare deux jeux de fichiers et renvoie les fichiers qui diffèrent.
func DiffFiles(have, want coreconf.Files) []FileChange {
	names := map[string]bool{}
	for n := range have {
		names[n] = true
	}
	for n := range want {
		names[n] = true
	}
	sorted := make([]string, 0, len(names))
	for n := range names {
		sorted = append(sorted, n)
	}
	sort.Strings(sorted)
	var out []FileChange
	for _, n := range sorted {
		if have[n] == want[n] {
			continue
		}
		out = append(out, FileChange{File: n, Lines: diffLines(splitLines(have[n]), splitLines(want[n]))})
	}
	return out
}

func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(s, "\n"), "\n")
}

// diffLines : différence ligne à ligne (plus longue sous-séquence commune ; fichiers courts),
// en ne gardant que 2 lignes de contexte autour des changements.
func diffLines(a, b []string) []DiffLine {
	n, m := len(a), len(b)
	lcs := make([][]int, n+1)
	for i := range lcs {
		lcs[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if a[i] == b[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else {
				lcs[i][j] = max(lcs[i+1][j], lcs[i][j+1])
			}
		}
	}
	var all []DiffLine
	i, j := 0, 0
	for i < n || j < m {
		switch {
		case i < n && j < m && a[i] == b[j]:
			all = append(all, DiffLine{" ", a[i]})
			i, j = i+1, j+1
		case i < n && (j == m || lcs[i+1][j] >= lcs[i][j+1]):
			// À égalité, la ligne retirée avant la ligne ajoutée (ordre habituel d'un diff)
			all = append(all, DiffLine{"-", a[i]})
			i++
		default:
			all = append(all, DiffLine{"+", b[j]})
			j++
		}
	}
	const ctx = 2
	keep := make([]bool, len(all))
	for k, d := range all {
		if d.Op != " " {
			for x := max(0, k-ctx); x <= min(len(all)-1, k+ctx); x++ {
				keep[x] = true
			}
		}
	}
	var out []DiffLine
	for k, d := range all {
		if keep[k] {
			out = append(out, d)
		} else if len(out) == 0 || out[len(out)-1].Op != "…" {
			out = append(out, DiffLine{"…", ""})
		}
	}
	return out
}
