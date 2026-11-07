package lab

import (
	"bufio"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var (
	// VAR="${VAR:-défaut}" dans config.env
	reDefault = regexp.MustCompile(`^([A-Z_]+)="\$\{[A-Z_]+:-(.*)\}"\s*(#.*)?$`)
	// VAR=valeur ou VAR="valeur" dans config.local.env
	reAssign = regexp.MustCompile(`^(?:export\s+)?([A-Z_]+)=("([^"]*)"|'([^']*)'|(\S*))\s*(#.*)?$`)
)

// Accesseurs des réglages lus à plusieurs endroits : seul endroit où leur nom et leur valeur par défaut sont écrits
// (référence et commentaires : config.env).

// VMUser : compte créé dans les VMs et utilisé pour s'y connecter (LABS_VM_USER).
func (l *Lab) VMUser() string { return l.Setting("LABS_VM_USER", "lab") }

// DefaultNetwork : réseau libvirt par défaut, sortie Internet de la passerelle (LABS_DEFAULT_NETWORK).
func (l *Lab) DefaultNetwork() string { return l.Setting("LABS_DEFAULT_NETWORK", "default") }

// DefaultImage : image cloud par défaut d'une VM (LABS_DEFAULT_IMAGE).
func (l *Lab) DefaultImage() string { return l.Setting("LABS_DEFAULT_IMAGE", "debian13") }

// Setting renvoie un réglage : variable d'environnement, puis config.local.env, puis la valeur par défaut
// de config.env (à la racine de Labs). Les défauts qui
// dépendent d'un calcul shell ($(...)) ne sont pas interprétés : fallback est alors utilisé.
// Les références $VAR / ${VAR} d'une valeur (ex. "$LABS_STORAGE_DIR/images") sont développées.
func (l *Lab) Setting(name, fallback string) string {
	return l.setting(name, fallback, 0)
}

func (l *Lab) setting(name, fallback string, depth int) string {
	raw, ok := "", false
	if v, set := os.LookupEnv(name); set && v != "" {
		raw, ok = v, true
	} else if v, set := readAssign(filepath.Join(l.Root, "config.local.env"), name); set {
		raw, ok = v, true
	} else if v, set := readDefault(filepath.Join(l.Root, "config.env"), name); set && !strings.Contains(v, "$(") {
		raw, ok = v, true
	}
	if !ok {
		return fallback
	}
	if depth > 5 {
		return raw
	}
	return expandHome(os.Expand(raw, func(ref string) string {
		switch ref {
		case "HOME":
			home, _ := os.UserHomeDir()
			return home
		case "LABS_ROOT": // racine de ce lab : chemins propres au lab, qui suivent le dossier s'il est déplacé
			return l.Root
		}
		return l.setting(ref, os.Getenv(ref), depth+1)
	}))
}

func readAssign(path, name string) (string, bool) {
	return scanFile(path, func(line string) (string, bool) {
		m := reAssign.FindStringSubmatch(line)
		if m == nil || m[1] != name {
			return "", false
		}
		return m[3] + m[4] + m[5], true
	})
}

func readDefault(path, name string) (string, bool) {
	return scanFile(path, func(line string) (string, bool) {
		m := reDefault.FindStringSubmatch(line)
		if m == nil || m[1] != name {
			return "", false
		}
		return m[2], true
	})
}

func scanFile(path string, match func(string) (string, bool)) (string, bool) {
	f, err := os.Open(path)
	if err != nil {
		return "", false
	}
	defer f.Close()
	val, found := "", false
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if v, ok := match(strings.TrimSpace(sc.Text())); ok {
			val, found = v, true // la dernière affectation l'emporte, comme en shell
		}
	}
	return val, found
}

func expandHome(v string) string {
	if home, err := os.UserHomeDir(); err == nil {
		v = strings.ReplaceAll(v, "$HOME", home)
		if strings.HasPrefix(v, "~/") {
			v = filepath.Join(home, v[2:])
		}
	}
	return v
}
