package lab

import (
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"labs/labctl/internal/modfiles"
	"labs/labctl/internal/module"
	"labs/labctl/internal/names"
)

// Fichiers complémentaires d'un module (modules/<m>/files/) : le travail est fait par le paquet modfiles ; le lab
// trouve le dossier du module et relie files/ aux entrées files: du module.yaml.

// filesBase renvoie modules/<m>/files (le dossier du module doit exister ; files/ peut manquer).
func (l *Lab) filesBase(name string) (string, error) {
	if !names.Label(name) {
		return "", fmt.Errorf("nom de module invalide : %q", name)
	}
	dir := filepath.Join(l.Root, "modules", name)
	if fi, err := os.Lstat(dir); err != nil || !fi.IsDir() {
		return "", fmt.Errorf("module inconnu : %s", name)
	}
	base := filepath.Join(dir, modfiles.FilesDir)
	if fi, err := os.Lstat(base); err == nil && !fi.IsDir() {
		return "", fmt.Errorf("%s/%s n'est pas un dossier", name, modfiles.FilesDir)
	}
	return base, nil
}

// moduleTree renvoie l'arborescence files/ d'un module existant.
func (l *Lab) moduleTree(name string) (modfiles.Tree, error) {
	base, err := l.filesBase(name)
	return modfiles.Tree{Base: base}, err
}

// ModuleFiles liste files/ et la relie aux entrées files: du module.yaml.
func (l *Lab) ModuleFiles(name string) (modfiles.FileList, error) {
	out := modfiles.FileList{Files: []modfiles.File{}}
	base, err := l.filesBase(name)
	if err != nil {
		return out, err
	}
	uses := map[string][]modfiles.FileRef{}
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
				ref := modfiles.FileRef{VM: vm, Src: f.Src, Dest: f.Dest, Template: f.Template}
				rel, ok := strings.CutPrefix(path.Clean(filepath.ToSlash(f.Src)), modfiles.FilesDir+"/")
				if filepath.IsAbs(f.Src) || !ok {
					out.Outside = append(out.Outside, ref)
					continue
				}
				uses[rel] = append(uses[rel], ref)
			}
		}
	}
	out.Files, out.Missing, err = modfiles.Tree{Base: base}.List(uses)
	return out, err
}

// ModuleFilePath renvoie le chemin d'un fichier de files/ (téléchargement).
func (l *Lab) ModuleFilePath(name, rel string) (string, error) {
	t, err := l.moduleTree(name)
	if err != nil {
		return "", err
	}
	return t.Path(rel)
}

// ModuleFileText renvoie le contenu d'un fichier texte de files/ et son empreinte.
func (l *Lab) ModuleFileText(name, rel string) (text, hash string, err error) {
	t, err := l.moduleTree(name)
	if err != nil {
		return "", "", err
	}
	return t.Text(rel)
}

// SaveModuleFileText enregistre un fichier texte de files/ (voir modfiles.Tree.SaveText).
func (l *Lab) SaveModuleFileText(name, rel, text, base string, create bool) (string, error) {
	t, err := l.moduleTree(name)
	if err != nil {
		return "", err
	}
	return t.SaveText(rel, text, base, create)
}

// UploadModuleFile enregistre un fichier envoyé dans files/.
func (l *Lab) UploadModuleFile(name, rel string, r io.Reader, overwrite bool) error {
	t, err := l.moduleTree(name)
	if err != nil {
		return err
	}
	return t.Upload(rel, r, overwrite)
}

// MkdirModuleFile crée un dossier sous files/.
func (l *Lab) MkdirModuleFile(name, rel string) error {
	t, err := l.moduleTree(name)
	if err != nil {
		return err
	}
	return t.Mkdir(rel)
}

// RenameModuleFile renomme ou déplace un fichier ou un dossier de files/.
func (l *Lab) RenameModuleFile(name, from, to string) error {
	t, err := l.moduleTree(name)
	if err != nil {
		return err
	}
	return t.Rename(from, to)
}

// DeleteModuleFile supprime un fichier ou un dossier de files/.
func (l *Lab) DeleteModuleFile(name, rel string) error {
	t, err := l.moduleTree(name)
	if err != nil {
		return err
	}
	return t.Delete(rel)
}
