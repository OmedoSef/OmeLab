// Package modfiles gère les fichiers complémentaires d'un module (modules/<m>/files/, copiés dans les VMs par
// files: du module.yaml) : liste, lecture, écriture, envoi, renommage, suppression depuis la GUI. Tout reste sous ce
// dossier : chemins relatifs nettoyés, sans « .. », et aucun lien symbolique n'est suivi (ni listé). Les écritures
// sont atomiques (fichier temporaire puis renommage), et un enregistrement sur une version périmée est refusé.
// WriteAtomic, TextHash et ErrConflict servent aussi ailleurs (module.yaml, network.local.yaml).
package modfiles

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

// FilesDir : dossier des fichiers d'un module, relatif au dossier du module.
const FilesDir = "files"

const (
	MaxTextFile = 1 << 20   // taille maximale d'un fichier éditable dans la GUI
	MaxUpload   = 512 << 20 // taille maximale d'un fichier envoyé
)

// ErrExists : la destination existe déjà.
var ErrExists = errors.New("existe déjà")

// File est une entrée de files/.
type File struct {
	Path string    `json:"path"` // relatif à files/, séparateur /
	Dir  bool      `json:"dir"`
	Size int64     `json:"size"`
	Mod  time.Time `json:"mod"`
	Text bool      `json:"text"`           // éditable dans la GUI (texte UTF-8, MaxTextFile au plus)
	Uses []FileRef `json:"uses,omitempty"` // entrées files: du module.yaml qui le copient
}

// FileRef est une entrée files: d'une VM du module.yaml.
type FileRef struct {
	VM       string `json:"vm"`
	Src      string `json:"src"`
	Dest     string `json:"dest"`
	Template bool   `json:"template,omitempty"`
}

// FileList : contenu de files/ et références du module.yaml.
type FileList struct {
	Files   []File    `json:"files"`
	Missing []FileRef `json:"missing,omitempty"` // référencés sous files/ mais absents
	Outside []FileRef `json:"outside,omitempty"` // référencés hors de files/ (non gérés ici)
	Warning string    `json:"warning,omitempty"` // module.yaml illisible : références inconnues
}

// ErrConflict : le fichier a changé depuis qu'il a été lu.
var ErrConflict = errors.New("le fichier a été modifié entre-temps (ailleurs que dans cet éditeur) : recharger avant d'enregistrer")

// TextHash est l'empreinte d'un contenu (détection des modifications concurrentes).
func TextHash(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// Tree est le dossier files/ d'un module (Base : chemin absolu ; il peut ne pas exister encore).
type Tree struct{ Base string }

// CleanRel valide un chemin relatif à files/ et le renvoie nettoyé (séparateur /).
func CleanRel(rel string) (string, error) {
	rel = strings.TrimSpace(strings.ReplaceAll(rel, "\\", "/"))
	if rel == "" || strings.ContainsRune(rel, 0) || strings.HasPrefix(rel, "/") {
		return "", fmt.Errorf("chemin invalide : %q", rel)
	}
	c := path.Clean(rel)
	if c == "." || c == ".." || strings.HasPrefix(c, "../") {
		return "", fmt.Errorf("chemin hors du dossier %s/ : %q", FilesDir, rel)
	}
	for _, part := range strings.Split(c, "/") {
		if len(part) > 255 {
			return "", fmt.Errorf("nom trop long : %q", part)
		}
	}
	return c, nil
}

// SafePath renvoie le chemin absolu de rel sous base, après avoir vérifié qu'aucun élément existant n'est un
// lien symbolique (base comprise) : impossible de sortir de files/.
func SafePath(base, rel string) (string, string, error) {
	c, err := CleanRel(rel)
	if err != nil {
		return "", "", err
	}
	p := base
	for _, part := range append([]string{""}, strings.Split(c, "/")...) {
		if part != "" {
			p = filepath.Join(p, part)
		}
		fi, err := os.Lstat(p)
		if errors.Is(err, fs.ErrNotExist) {
			break // la suite n'existe pas encore (création)
		}
		if err != nil {
			return "", "", err
		}
		if fi.Mode()&fs.ModeSymlink != 0 {
			return "", "", fmt.Errorf("lien symbolique refusé : %s", c)
		}
	}
	return filepath.Join(base, filepath.FromSlash(c)), c, nil
}

// isText : contenu éditable dans la GUI (UTF-8, sans octet nul, MaxTextFile au plus).
func isText(b []byte) bool {
	if len(b) > MaxTextFile || !utf8.Valid(b) {
		return false
	}
	head := b
	if len(head) > 8192 {
		head = head[:8192]
	}
	return !bytes.ContainsRune(head, 0)
}

// List parcourt files/ et relie chaque fichier aux entrées files: qui le copient (uses : chemin relatif à files/ ->
// entrées) ; renvoie aussi les entrées dont le fichier manque. Les liens symboliques ne sont ni listés ni suivis.
func (t Tree) List(uses map[string][]FileRef) (files []File, missing []FileRef, err error) {
	base := t.Base
	out := FileList{Files: []File{}}
	if _, err := os.Lstat(base); errors.Is(err, fs.ErrNotExist) {
		for _, refs := range uses {
			out.Missing = append(out.Missing, refs...)
		}
		return out.Files, out.Missing, nil
	}
	seen := map[string]bool{}
	err = filepath.WalkDir(base, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == base || d.Type()&fs.ModeSymlink != 0 {
			return nil // les liens ne sont ni listés ni suivis
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		rel := filepath.ToSlash(strings.TrimPrefix(p, base+string(filepath.Separator)))
		f := File{Path: rel, Dir: d.IsDir(), Mod: info.ModTime()}
		if !d.IsDir() {
			if !info.Mode().IsRegular() {
				return nil
			}
			f.Size = info.Size()
			if f.Size <= MaxTextFile {
				if b, err := os.ReadFile(p); err == nil {
					f.Text = isText(b)
				}
			}
			f.Uses, seen[rel] = uses[rel], true
		}
		out.Files = append(out.Files, f)
		return nil
	})
	for rel, refs := range uses {
		if !seen[rel] {
			out.Missing = append(out.Missing, refs...)
		}
	}
	sort.Slice(out.Missing, func(i, j int) bool { return out.Missing[i].Src < out.Missing[j].Src })
	return out.Files, out.Missing, err
}

// regular ouvre rel en lecture : un fichier ordinaire uniquement.
func (t Tree) regular(rel string) (string, os.FileInfo, error) {
	base := t.Base
	p, c, err := SafePath(base, rel)
	if err != nil {
		return "", nil, err
	}
	fi, err := os.Lstat(p)
	if err != nil {
		return "", nil, fmt.Errorf("%s/%s introuvable", FilesDir, c)
	}
	if !fi.Mode().IsRegular() {
		return "", nil, fmt.Errorf("%s/%s n'est pas un fichier", FilesDir, c)
	}
	return p, fi, nil
}

// Path renvoie le chemin d'un fichier de files/ (téléchargement).
func (t Tree) Path(rel string) (string, error) {
	p, _, err := t.regular(rel)
	return p, err
}

// Text renvoie le contenu d'un fichier texte de files/ et son empreinte.
func (t Tree) Text(rel string) (text, hash string, err error) {
	p, _, err := t.regular(rel)
	if err != nil {
		return "", "", err
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return "", "", err
	}
	if !isText(b) {
		return "", "", fmt.Errorf("pas un fichier texte éditable (binaire ou plus de %d Kio) : le télécharger", MaxTextFile>>10)
	}
	return string(b), TextHash(b), nil
}

// WriteAtomic écrit r dans p par un fichier temporaire du même dossier, créé en exclusif, puis renommé : jamais de
// fichier à moitié écrit, et un lien posé à la place de p (ou d'un nom temporaire prévisible) est remplacé, jamais
// suivi.
// Le mode d'un fichier remplacé est conservé.
func WriteAtomic(p string, r io.Reader, limit int64) error {
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	mode := fs.FileMode(0o644)
	if fi, err := os.Lstat(p); err == nil && fi.Mode().IsRegular() { // jamais les droits d'un lien (0777)
		mode = fi.Mode().Perm()
	}
	tmp, err := os.CreateTemp(filepath.Dir(p), ".labctl-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	n, err := io.Copy(tmp, io.LimitReader(r, limit+1))
	if err == nil && n > limit {
		err = fmt.Errorf("fichier trop gros (%d Mio au plus)", limit>>20)
	}
	if err == nil {
		err = tmp.Chmod(mode)
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	return os.Rename(tmp.Name(), p)
}

// SaveText enregistre un fichier texte. create : le fichier ne doit pas exister ; sinon base est
// l'empreinte lue (ErrConflict si le fichier a changé entre-temps). Renvoie la nouvelle empreinte.
func (t Tree) SaveText(rel, text, base string, create bool) (string, error) {
	dir := t.Base
	p, c, err := SafePath(dir, rel)
	if err != nil {
		return "", err
	}
	if len(text) > MaxTextFile {
		return "", fmt.Errorf("texte trop long (%d Kio au plus)", MaxTextFile>>10)
	}
	cur, rerr := os.ReadFile(p)
	switch {
	case create && rerr == nil:
		return "", fmt.Errorf("%s/%s : %w", FilesDir, c, ErrExists)
	case !create && rerr != nil:
		return "", fmt.Errorf("%s/%s introuvable", FilesDir, c)
	case !create && TextHash(cur) != base:
		return "", ErrConflict
	}
	if fi, err := os.Lstat(p); err == nil && !fi.Mode().IsRegular() {
		return "", fmt.Errorf("%s/%s n'est pas un fichier", FilesDir, c)
	}
	if err := WriteAtomic(p, strings.NewReader(text), MaxTextFile); err != nil {
		return "", err
	}
	return TextHash([]byte(text)), nil
}

// Upload enregistre un fichier envoyé (ErrExists s'il existe et que overwrite est faux).
func (t Tree) Upload(rel string, r io.Reader, overwrite bool) error {
	dir := t.Base
	p, c, err := SafePath(dir, rel)
	if err != nil {
		return err
	}
	if fi, err := os.Lstat(p); err == nil {
		if !fi.Mode().IsRegular() {
			return fmt.Errorf("%s/%s existe et n'est pas un fichier", FilesDir, c)
		}
		if !overwrite {
			return fmt.Errorf("%s/%s : %w", FilesDir, c, ErrExists)
		}
	}
	return WriteAtomic(p, r, MaxUpload)
}

// Mkdir crée un dossier (et ses parents) sous files/.
func (t Tree) Mkdir(rel string) error {
	dir := t.Base
	p, c, err := SafePath(dir, rel)
	if err != nil {
		return err
	}
	if _, err := os.Lstat(p); err == nil {
		return fmt.Errorf("%s/%s : %w", FilesDir, c, ErrExists)
	}
	return os.MkdirAll(p, 0o755)
}

// Rename renomme ou déplace un fichier ou un dossier de files/ (la destination ne doit pas exister).
func (t Tree) Rename(from, to string) error {
	dir := t.Base
	src, cf, err := SafePath(dir, from)
	if err != nil {
		return err
	}
	dst, ct, err := SafePath(dir, to)
	if err != nil {
		return err
	}
	if _, err := os.Lstat(src); err != nil {
		return fmt.Errorf("%s/%s introuvable", FilesDir, cf)
	}
	if _, err := os.Lstat(dst); err == nil {
		return fmt.Errorf("%s/%s : %w", FilesDir, ct, ErrExists)
	}
	if strings.HasPrefix(ct+"/", cf+"/") {
		return fmt.Errorf("impossible de déplacer %s dans lui-même", cf)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	return os.Rename(src, dst)
}

// Delete supprime un fichier ou un dossier (avec son contenu) de files/.
func (t Tree) Delete(rel string) error {
	dir := t.Base
	p, c, err := SafePath(dir, rel)
	if err != nil {
		return err
	}
	if _, err := os.Lstat(p); err != nil {
		return fmt.Errorf("%s/%s introuvable", FilesDir, c)
	}
	return os.RemoveAll(p) // SafePath a refusé tout lien sur le chemin ; RemoveAll ne suit pas ceux du contenu
}
