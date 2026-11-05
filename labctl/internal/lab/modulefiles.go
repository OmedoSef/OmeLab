package lab

import (
	"bytes"
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

	"labs/labctl/internal/module"
)

// Fichiers complémentaires d'un module (modules/<m>/files/, copiés dans les VMs par files: du module.yaml) :
// liste, lecture, écriture, envoi, renommage, suppression depuis la GUI. Tout reste sous ce dossier : chemins
// relatifs nettoyés, sans « .. », et aucun lien symbolique n'est suivi (ni listé).

// FilesDir : dossier des fichiers d'un module, relatif au dossier du module.
const FilesDir = "files"

const (
	MaxTextFile = 1 << 20   // taille maximale d'un fichier éditable dans la GUI
	MaxUpload   = 512 << 20 // taille maximale d'un fichier envoyé
)

// ErrExists : la destination existe déjà.
var ErrExists = errors.New("existe déjà")

// ModuleFile est une entrée de files/.
type ModuleFile struct {
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
	Files   []ModuleFile `json:"files"`
	Missing []FileRef    `json:"missing,omitempty"` // référencés sous files/ mais absents
	Outside []FileRef    `json:"outside,omitempty"` // référencés hors de files/ (non gérés ici)
	Warning string       `json:"warning,omitempty"` // module.yaml illisible : références inconnues
}

// filesBase renvoie modules/<m>/files (le dossier du module doit exister ; files/ peut manquer).
func (l *Lab) filesBase(name string) (string, error) {
	if !reModuleName.MatchString(name) {
		return "", fmt.Errorf("nom de module invalide : %q", name)
	}
	dir := filepath.Join(l.Root, "modules", name)
	if fi, err := os.Lstat(dir); err != nil || !fi.IsDir() {
		return "", fmt.Errorf("module inconnu : %s", name)
	}
	base := filepath.Join(dir, FilesDir)
	if fi, err := os.Lstat(base); err == nil && !fi.IsDir() {
		return "", fmt.Errorf("%s/%s n'est pas un dossier", name, FilesDir)
	}
	return base, nil
}

// cleanRel valide un chemin relatif à files/ et le renvoie nettoyé (séparateur /).
func cleanRel(rel string) (string, error) {
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

// safePath renvoie le chemin absolu de rel sous base, après avoir vérifié qu'aucun élément existant n'est un
// lien symbolique (base comprise) : impossible de sortir de files/.
func safePath(base, rel string) (string, string, error) {
	c, err := cleanRel(rel)
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

// ModuleFiles liste files/ et la relie aux entrées files: du module.yaml.
func (l *Lab) ModuleFiles(name string) (FileList, error) {
	out := FileList{Files: []ModuleFile{}}
	base, err := l.filesBase(name)
	if err != nil {
		return out, err
	}
	uses := map[string][]FileRef{}
	if y, err := module.Load(filepath.Dir(base)); err != nil {
		out.Warning = "module.yaml illisible : fichiers utilisés inconnus (" + err.Error() + ")"
	} else {
		vms := make([]string, 0, len(y.VMs))
		for vm := range y.VMs {
			vms = append(vms, vm)
		}
		sort.Strings(vms)
		for _, vm := range vms {
			for _, f := range y.VMs[vm].Files {
				ref := FileRef{VM: vm, Src: f.Src, Dest: f.Dest, Template: f.Template}
				rel, ok := strings.CutPrefix(path.Clean(filepath.ToSlash(f.Src)), FilesDir+"/")
				if filepath.IsAbs(f.Src) || !ok {
					out.Outside = append(out.Outside, ref)
					continue
				}
				uses[rel] = append(uses[rel], ref)
			}
		}
	}
	if _, err := os.Lstat(base); errors.Is(err, fs.ErrNotExist) {
		for _, refs := range uses {
			out.Missing = append(out.Missing, refs...)
		}
		return out, nil
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
		f := ModuleFile{Path: rel, Dir: d.IsDir(), Mod: info.ModTime()}
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
	return out, err
}

// regular ouvre rel en lecture : un fichier ordinaire uniquement.
func (l *Lab) regular(name, rel string) (string, os.FileInfo, error) {
	base, err := l.filesBase(name)
	if err != nil {
		return "", nil, err
	}
	p, c, err := safePath(base, rel)
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

// ModuleFilePath renvoie le chemin d'un fichier de files/ (téléchargement).
func (l *Lab) ModuleFilePath(name, rel string) (string, error) {
	p, _, err := l.regular(name, rel)
	return p, err
}

// ModuleFileText renvoie le contenu d'un fichier texte de files/ et son empreinte.
func (l *Lab) ModuleFileText(name, rel string) (text, hash string, err error) {
	p, _, err := l.regular(name, rel)
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

// writeAtomic écrit r dans p par un fichier temporaire du même dossier (jamais de fichier à moitié écrit).
// Le mode d'un fichier remplacé est conservé.
func writeAtomic(p string, r io.Reader, limit int64) error {
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	mode := fs.FileMode(0o644)
	if fi, err := os.Lstat(p); err == nil {
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

// SaveModuleFileText enregistre un fichier texte. create : le fichier ne doit pas exister ; sinon base est
// l'empreinte lue (ErrConflict si le fichier a changé entre-temps). Renvoie la nouvelle empreinte.
func (l *Lab) SaveModuleFileText(name, rel, text, base string, create bool) (string, error) {
	dir, err := l.filesBase(name)
	if err != nil {
		return "", err
	}
	p, c, err := safePath(dir, rel)
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
	if err := writeAtomic(p, strings.NewReader(text), MaxTextFile); err != nil {
		return "", err
	}
	return TextHash([]byte(text)), nil
}

// UploadModuleFile enregistre un fichier envoyé (ErrExists s'il existe et que overwrite est faux).
func (l *Lab) UploadModuleFile(name, rel string, r io.Reader, overwrite bool) error {
	dir, err := l.filesBase(name)
	if err != nil {
		return err
	}
	p, c, err := safePath(dir, rel)
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
	return writeAtomic(p, r, MaxUpload)
}

// MkdirModuleFile crée un dossier (et ses parents) sous files/.
func (l *Lab) MkdirModuleFile(name, rel string) error {
	dir, err := l.filesBase(name)
	if err != nil {
		return err
	}
	p, c, err := safePath(dir, rel)
	if err != nil {
		return err
	}
	if _, err := os.Lstat(p); err == nil {
		return fmt.Errorf("%s/%s : %w", FilesDir, c, ErrExists)
	}
	return os.MkdirAll(p, 0o755)
}

// RenameModuleFile renomme ou déplace un fichier ou un dossier de files/ (la destination ne doit pas exister).
func (l *Lab) RenameModuleFile(name, from, to string) error {
	dir, err := l.filesBase(name)
	if err != nil {
		return err
	}
	src, cf, err := safePath(dir, from)
	if err != nil {
		return err
	}
	dst, ct, err := safePath(dir, to)
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

// DeleteModuleFile supprime un fichier ou un dossier (avec son contenu) de files/.
func (l *Lab) DeleteModuleFile(name, rel string) error {
	dir, err := l.filesBase(name)
	if err != nil {
		return err
	}
	p, c, err := safePath(dir, rel)
	if err != nil {
		return err
	}
	if _, err := os.Lstat(p); err != nil {
		return fmt.Errorf("%s/%s introuvable", FilesDir, c)
	}
	return os.RemoveAll(p) // ne suit pas les liens : un lien éventuel est retiré, pas sa cible
}
