package lab

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"labs/labctl/internal/modfiles"
	"labs/labctl/internal/module"
	"labs/labctl/internal/names"
)

// Édition d'un module.yaml en texte (éditeur de la GUI) : le texte est validé tel quel, et seul un texte
// sans erreur est enregistré. Un enregistrement refuse d'écraser un fichier modifié entre-temps.

// ErrInvalid : le texte contient des erreurs.
var ErrInvalid = errors.New("enregistrement refusé : le module contient des erreurs")

var (
	reYAMLLine = regexp.MustCompile(`ligne (\d+)`)
)

func (l *Lab) moduleFile(name string) string {
	return filepath.Join(l.Root, "modules", name, module.FileName)
}

// ModuleText renvoie le contenu du module.yaml d'un module et son empreinte.
func (l *Lab) ModuleText(name string) (text, hash string, err error) {
	if !names.Label(name) {
		return "", "", fmt.Errorf("nom de module invalide : %q", name)
	}
	b, err := os.ReadFile(l.moduleFile(name))
	if errors.Is(err, os.ErrNotExist) {
		return "", "", fmt.Errorf("le module %s n'a pas de %s", name, module.FileName)
	}
	if err != nil {
		return "", "", err
	}
	return string(b), modfiles.TextHash(b), nil
}

// CheckModuleText valide un texte de module.yaml sans l'enregistrer. Une erreur de lecture (syntaxe,
// clé inconnue, type) devient un problème placé à sa ligne.
func (l *Lab) CheckModuleText(name, text string) []module.Issue {
	if !names.Label(name) { // nom venu de la GUI : jamais un chemin
		return []module.Issue{{Error: true, Msg: fmt.Sprintf("nom de module invalide : %q (minuscules, chiffres, tirets)", name)}}
	}
	y, err := module.Parse([]byte(text), filepath.Join(l.Root, "modules", name))
	if err != nil {
		var issues []module.Issue
		// Plusieurs erreurs de type sont séparées par « ; » : une par ligne concernée
		for _, part := range strings.Split(err.Error(), " ; ") {
			i := module.Issue{Error: true, Msg: part}
			if m := reYAMLLine.FindStringSubmatch(part); m != nil {
				i.Line, _ = strconv.Atoi(m[1])
			}
			issues = append(issues, i)
		}
		return issues
	}
	return y.Validate(l.ModuleContext(name))
}

// SaveModuleText enregistre un module.yaml après validation. base est l'empreinte du fichier lu (vide pour
// créer un nouveau module : le dossier ne doit pas exister). Renvoie l'empreinte du fichier enregistré.
func (l *Lab) SaveModuleText(name, text, base string, create bool) (string, []module.Issue, error) {
	if !names.Label(name) {
		return "", nil, fmt.Errorf("nom de module invalide : %q (minuscules, chiffres, tirets)", name)
	}
	path := l.moduleFile(name)
	dir := filepath.Dir(path)
	if create {
		if len(name) > 9 || slices.Contains(module.ReservedNames, name) {
			return "", nil, fmt.Errorf("nom %q : 9 caractères au plus, et pas un nom réservé (%s)", name, strings.Join(module.ReservedNames, ", "))
		}
		if _, err := os.Stat(dir); err == nil {
			return "", nil, fmt.Errorf("le dossier modules/%s existe déjà", name)
		}
	} else {
		cur, err := os.ReadFile(path)
		if err != nil {
			return "", nil, err
		}
		if modfiles.TextHash(cur) != base {
			return "", nil, modfiles.ErrConflict
		}
	}
	if !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	issues := l.CheckModuleText(name, text)
	if module.Errors(issues) > 0 {
		return "", issues, ErrInvalid
	}
	if create {
		if err := os.MkdirAll(filepath.Join(dir, "files"), 0o755); err != nil {
			return "", nil, err
		}
		l.ensureSchema() // la 1re ligne du squelette y renvoie : l'éditeur doit le trouver
	}
	if err := module.NotSymlink(path); err != nil {
		return "", nil, err
	}
	if err := modfiles.WriteAtomic(path, strings.NewReader(text), modfiles.MaxTextFile); err != nil { // jamais par un lien
		return "", nil, err
	}
	return modfiles.TextHash([]byte(text)), issues, nil
}

// ensureSchema pose le schéma de module.yaml à la racine du lab s'il n'y est pas (lab créé avant qu'init le pose).
// Sans effet si un fichier (ou un lien, dans le dépôt de développement) est déjà là ; une erreur est sans gravité.
func (l *Lab) ensureSchema() {
	p := filepath.Join(l.Root, module.SchemaFile)
	if _, err := os.Lstat(p); os.IsNotExist(err) {
		_ = os.WriteFile(p, module.SchemaJSON, 0o644)
	}
}

// ModuleSkeleton renvoie un module.yaml commenté pour un nouveau module. Sa 1re ligne désigne le schéma à l'éditeur.
func ModuleSkeleton(name string) string {
	return module.SchemaModeline + "\n" + strings.ReplaceAll(`# Module NAME. Voir docs/module-yaml.md pour toutes les clés.
module: NAME
description: À décrire

# network:                    # facultatif : sans section, un /24 libre est choisi au premier « up »
#   subnet: 10.70.70.0/24     # hôte .1, VMs à IP fixe .2-.99, DHCP libre .100-.199, passerelle core .254
#   subdomain: NAME           # domaine = NAME.<racine du lab> ; sinon défaut <réseau>.<racine>
#   # domain: NAME.local      # ... ou un domaine complet, pris tel quel (ignore la racine du lab)

# pki:
#   ca: NAME                  # CA dérivée, limitée au domaine et au sous-réseau du module

# defaults:                   # valeurs communes aux VMs (sinon : réglages, puis 1 vCPU, 2048 Mio, 20 Gio, debian13)
#   memory: 2048

# dns:
#   aliases:                  # <alias>.<domaine> -> VM du module
#     www: srv
#   links: [pentest]          # modules que celui-ci peut joindre et résoudre (tout, tous ports)

# connections:                # accès restreints : vers un module, module/vm, ou internet (quand il est ouvert)
#   - {to: pentest/web, from: [srv], ports: [tcp/80, tcp/443]}
#   - {to: internet, ports: [tcp/443, udp/53]}

vms:
  srv:                        # VM NAME-srv, nom DNS srv.<domaine>
    ip: 10                    # IP fixe .10 (sans ip : DHCP libre) ; MAC ajoutée au premier « up »
    # image: ubuntu2404       # debian12, debian13, ubuntu2404...
    # cpus: 2
    # internet: install       # off (défaut) | install (le temps de l'installation) | never
    # packages: [nginx]
    # files:
    #   - {src: files/index.html, dest: /var/www/html/index.html, template: true}
    # run:
    #   - systemctl restart nginx
    snapshot: clean
`, "NAME", name)
}
