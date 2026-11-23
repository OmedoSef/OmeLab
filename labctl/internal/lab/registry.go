package lab

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"

	"labs/labctl/internal/modfiles"
	"labs/labctl/internal/names"
)

// Registre des labs de l'utilisateur : les labs connus par leur nom, et celui piloté par défaut. Il ne sert qu'à
// choisir un lab (labctl use) ; chaque lab reste un dossier autonome. Un seul lab tourne à la fois sur une machine :
// tous partagent le libvirt de l'hôte, la passerelle core-gw et les noms de VMs.

// Registry est le contenu de RegistryFile.
type Registry struct {
	Default string            `yaml:"default,omitempty"` // nom du lab piloté par défaut
	Labs    map[string]string `yaml:"labs,omitempty"`    // nom -> dossier (chemin absolu)
}

// RegistryFile : $XDG_CONFIG_HOME/labctl/labs.yaml (~/.config/labctl/labs.yaml).
func RegistryFile() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		dir = filepath.Join(os.Getenv("HOME"), ".config")
	}
	return filepath.Join(dir, "labctl", "labs.yaml")
}

// LoadRegistry lit le registre (vide s'il n'existe pas encore).
func LoadRegistry() (*Registry, error) {
	r := &Registry{Labs: map[string]string{}}
	b, err := os.ReadFile(RegistryFile())
	if errors.Is(err, os.ErrNotExist) {
		return r, nil
	}
	if err != nil {
		return nil, err
	}
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	if err := dec.Decode(r); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("%s illisible : %w", RegistryFile(), err)
	}
	if r.Labs == nil {
		r.Labs = map[string]string{}
	}
	return r, nil
}

// Save écrit le registre (écriture atomique).
func (r *Registry) Save() error {
	b, err := yaml.Marshal(r)
	if err != nil {
		return err
	}
	f := RegistryFile()
	if err := os.MkdirAll(filepath.Dir(f), 0o755); err != nil {
		return err
	}
	head := "# Labs connus de labctl (labctl use) : nom -> dossier, et le lab piloté par défaut.\n"
	return modfiles.WriteAtomic(f, strings.NewReader(head+string(b)), 1<<20)
}

// Names renvoie les noms des labs connus, triés.
func (r *Registry) Names() []string {
	out := make([]string, 0, len(r.Labs))
	for n := range r.Labs {
		out = append(out, n)
	}
	slices.Sort(out)
	return out
}

// NameOf renvoie le nom sous lequel le dossier root est enregistré ("" s'il ne l'est pas).
func (r *Registry) NameOf(root string) string {
	for n, p := range r.Labs {
		if filepath.Clean(p) == filepath.Clean(root) {
			return n
		}
	}
	return ""
}

// DefaultLab renvoie le lab par défaut (nom, dossier ; vides s'il n'y en a pas). Un lab par défaut dont le dossier
// n'existe plus est une erreur qui dit quoi faire.
func (r *Registry) DefaultLab() (name, root string, err error) {
	if r.Default == "" {
		return "", "", nil
	}
	root, ok := r.Labs[r.Default]
	if !ok {
		return "", "", fmt.Errorf("lab par défaut « %s » absent de %s : « labctl use NOM » en choisit un autre", r.Default, RegistryFile())
	}
	if !isRoot(root) {
		return "", "", fmt.Errorf("le lab par défaut « %s » (%s) n'existe plus : « labctl use NOM » en choisit un autre, « labctl use --forget %s » le retire", r.Default, root, r.Default)
	}
	return r.Default, root, nil
}

var reLabSanitize = regexp.MustCompile(`[^a-z0-9-]+`)

// LabName propose un nom de lab tiré du nom de son dossier (minuscules, chiffres, tirets).
func LabName(dir string) string {
	n := strings.Trim(reLabSanitize.ReplaceAllString(strings.ToLower(filepath.Base(dir)), "-"), "-")
	if n == "" {
		n = "lab"
	}
	return n
}

// Add enregistre le dossier dir sous le nom name (vide : celui du dossier, LabName). Un dossier déjà enregistré
// garde son nom si aucun n'est demandé ; un nom déjà pris par un autre dossier est refusé. Renvoie le nom retenu.
func (r *Registry) Add(name, dir string) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	if !isRoot(abs) {
		return "", fmt.Errorf("%s n'est pas un lab (config.env et modules/ attendus) : « labctl init %s » en crée un", abs, dir)
	}
	if name == "" {
		if old := r.NameOf(abs); old != "" {
			return old, nil
		}
		name = LabName(abs)
	}
	if !names.Label(name) {
		return "", fmt.Errorf("nom de lab invalide : %q (minuscules, chiffres, tirets)", name)
	}
	if p, ok := r.Labs[name]; ok && filepath.Clean(p) != abs {
		return "", fmt.Errorf("le nom « %s » désigne déjà %s : en choisir un autre (--as NOM)", name, p)
	}
	if old := r.NameOf(abs); old != "" && old != name { // renommage
		delete(r.Labs, old)
		if r.Default == old {
			r.Default = name
		}
	}
	r.Labs[name] = abs
	return name, nil
}

// Forget retire un lab du registre (son dossier n'est pas touché).
func (r *Registry) Forget(name string) error {
	if _, ok := r.Labs[name]; !ok {
		return fmt.Errorf("lab inconnu : %s (%s)", name, strings.Join(r.Names(), ", "))
	}
	delete(r.Labs, name)
	if r.Default == name {
		r.Default = ""
	}
	return nil
}
