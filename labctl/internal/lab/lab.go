// Package lab donne accès aux labs : découverte des modules, lecture de leur configuration
// (module.env, core.conf, en-tête d'aide de module.sh) et exécution de leurs commandes.
//
// Les scripts bash des modules restent la source de vérité : labctl les lit et les appelle,
// il ne réimplémente pas leur logique.
package lab

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
)

// Lab est la racine d'une arborescence Labs (dossier contenant lab.sh et modules/).
type Lab struct {
	Root    string
	Verbose bool // affiche sur stderr les commandes lancées
}

// Find localise la racine : $LABS_ROOT (posé par activate), sinon en remontant depuis l'exécutable,
// sinon depuis le dossier courant.
func Find() (*Lab, error) {
	if r := os.Getenv("LABS_ROOT"); r != "" {
		if isRoot(r) {
			return &Lab{Root: r}, nil
		}
		return nil, fmt.Errorf("LABS_ROOT=%s n'est pas une racine Labs (lab.sh et modules/ attendus)", r)
	}
	var starts []string
	if exe, err := os.Executable(); err == nil {
		if real, err := filepath.EvalSymlinks(exe); err == nil {
			starts = append(starts, filepath.Dir(real))
		}
	}
	if wd, err := os.Getwd(); err == nil {
		starts = append(starts, wd)
	}
	for _, dir := range starts {
		for d := dir; ; d = filepath.Dir(d) {
			if isRoot(d) {
				return &Lab{Root: d}, nil
			}
			if d == filepath.Dir(d) {
				break
			}
		}
	}
	return nil, errors.New("racine Labs introuvable : faire « source ./activate » depuis le dossier Labs")
}

func isRoot(d string) bool {
	st, err := os.Stat(filepath.Join(d, "modules"))
	if err != nil || !st.IsDir() {
		return false
	}
	_, err = os.Stat(filepath.Join(d, "lab.sh"))
	return err == nil
}

// Module décrit un module (dossier modules/<nom>/ avec un module.sh).
type Module struct {
	Name        string
	Dir         string
	Description string    // ligne 2 de module.sh
	Env         *Env      // nil pour un module sans module.env (ex. core)
	Commands    *CmdNode  // arbre des commandes, tiré de l'en-tête d'aide de module.sh
	Conf        *CoreConf // core.conf du module (vide s'il n'existe pas)
}

// Env reprend les variables utiles de module.env.
type Env struct {
	Net        string
	Subnet     string
	Domain     string
	Hosts      map[string]int    // VM -> dernier octet de l'IP réservée
	Macs       map[string]string // VM -> MAC
	NoInternet []string
}

// IP renvoie l'adresse réservée d'une VM du module, ou "" si elle n'en a pas.
func (e *Env) IP(vm string) string {
	if o, ok := e.Hosts[vm]; ok {
		return fmt.Sprintf("%s.%d", e.Subnet, o)
	}
	return ""
}

// VMs renvoie les VMs du module triées par adresse.
func (e *Env) VMs() []string {
	vms := make([]string, 0, len(e.Hosts))
	for vm := range e.Hosts {
		vms = append(vms, vm)
	}
	sort.Slice(vms, func(i, j int) bool { return e.Hosts[vms[i]] < e.Hosts[vms[j]] })
	return vms
}

// CoreConf reprend core.conf : réservations en plus, alias DNS, liens.
type CoreConf struct {
	Hosts   map[string]int
	Aliases map[string]string
	Links   []string
}

// LibvirtName donne le nom libvirt d'une VM de module (convention <module>-<vm>).
func (m *Module) LibvirtName(vm string) string { return m.Name + "-" + vm }

// Modules liste les modules, triés par nom.
func (l *Lab) Modules() ([]*Module, error) {
	entries, err := os.ReadDir(filepath.Join(l.Root, "modules"))
	if err != nil {
		return nil, err
	}
	pkiDomain := l.pkiDomain()
	var mods []*Module
	for _, e := range entries {
		dir := filepath.Join(l.Root, "modules", e.Name())
		script := filepath.Join(dir, "module.sh")
		if _, err := os.Stat(script); err != nil {
			continue
		}
		m := &Module{Name: e.Name(), Dir: dir}
		header, err := readHeader(script)
		if err != nil {
			return nil, err
		}
		m.Description = header.description
		m.Commands = ParseUsage(m.Name, header.lines)
		if env, err := ParseEnv(filepath.Join(dir, "module.env"), pkiDomain); err == nil {
			m.Env = env
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("module %s : %w", m.Name, err)
		}
		m.Conf, _ = ParseCoreConf(filepath.Join(dir, "core.conf"))
		mods = append(mods, m)
	}
	return mods, nil
}

// Module renvoie un module par son nom.
func (l *Lab) Module(name string) (*Module, error) {
	mods, err := l.Modules()
	if err != nil {
		return nil, err
	}
	for _, m := range mods {
		if m.Name == name {
			return m, nil
		}
	}
	return nil, fmt.Errorf("module inconnu : %s", name)
}

// pkiDomain lit le domaine racine par défaut dans scripts/config.env.
func (l *Lab) pkiDomain() string {
	re := regexp.MustCompile(`LABS_PKI_DOMAIN="\$\{LABS_PKI_DOMAIN:-([^}"]+)\}"`)
	if b, err := os.ReadFile(filepath.Join(l.Root, "scripts", "config.env")); err == nil {
		if m := re.FindSubmatch(b); m != nil {
			return string(m[1])
		}
	}
	return "omedo.lab"
}

type header struct {
	description string
	lines       []string
}

// readHeader lit le bloc de commentaires en tête d'un module.sh (jusqu'à la première ligne non commentée).
func readHeader(path string) (header, error) {
	f, err := os.Open(path)
	if err != nil {
		return header{}, err
	}
	defer f.Close()
	var h header
	sc := bufio.NewScanner(f)
	for n := 1; sc.Scan(); n++ {
		line := sc.Text()
		if n == 1 && strings.HasPrefix(line, "#!") {
			continue
		}
		if !strings.HasPrefix(line, "#") {
			break
		}
		if strings.HasPrefix(line, "# shellcheck") {
			continue
		}
		if n == 2 {
			h.description = strings.TrimSpace(strings.TrimPrefix(line, "#"))
		}
		h.lines = append(h.lines, line)
	}
	return h, sc.Err()
}

var (
	reVar   = regexp.MustCompile(`^([A-Z_]+)=("?)([^"]*)("?)\s*(#.*)?$`)
	reAssoc = regexp.MustCompile(`^declare -A ([A-Z_]+)=\((.*)\)`)
	reItem  = regexp.MustCompile(`\[([A-Za-z0-9_-]+)\]=([^\s)]+)`)
)

// ParseEnv lit un module.env (syntaxe bash simple : VAR=valeur et declare -A VAR=([cle]=valeur ...)).
func ParseEnv(path, pkiDomain string) (*Env, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	env := &Env{Hosts: map[string]int{}, Macs: map[string]string{}}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if m := reAssoc.FindStringSubmatch(line); m != nil {
			for _, it := range reItem.FindAllStringSubmatch(m[2], -1) {
				switch m[1] {
				case "HOSTS":
					o, err := strconv.Atoi(it[2])
					if err != nil {
						return nil, fmt.Errorf("HOSTS[%s] : octet invalide %q", it[1], it[2])
					}
					env.Hosts[it[1]] = o
				case "MACS":
					env.Macs[it[1]] = strings.ToLower(it[2])
				}
			}
			continue
		}
		if m := reVar.FindStringSubmatch(line); m != nil {
			switch m[1] {
			case "NET":
				env.Net = m[3]
			case "SUBNET":
				env.Subnet = m[3]
			case "DOMAIN":
				env.Domain = m[3]
			case "NO_INTERNET":
				env.NoInternet = strings.Fields(m[3])
			}
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if env.Net == "" || env.Subnet == "" {
		return nil, fmt.Errorf("%s : NET et SUBNET sont obligatoires", path)
	}
	if env.Domain == "" {
		env.Domain = env.Net + "." + pkiDomain
	}
	return env, nil
}

// ParseCoreConf lit un core.conf ; un fichier absent donne une configuration vide.
func ParseCoreConf(path string) (*CoreConf, error) {
	c := &CoreConf{Hosts: map[string]int{}, Aliases: map[string]string{}}
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return c, nil
		}
		return c, err
	}
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) == 0 || strings.HasPrefix(f[0], "#") {
			continue
		}
		switch {
		case f[0] == "host" && len(f) >= 3:
			if o, err := strconv.Atoi(f[2]); err == nil {
				c.Hosts[f[1]] = o
			}
		case f[0] == "alias" && len(f) >= 3:
			c.Aliases[f[1]] = f[2]
		case f[0] == "link" && len(f) >= 2:
			c.Links = append(c.Links, f[1])
		}
	}
	return c, nil
}

// Exec remplace le processus courant par « lab.sh <module> <args...> » : entrées et sorties
// (y compris un terminal interactif pour ssh) sont celles de labctl.
func (l *Lab) Exec(args ...string) error {
	return l.ExecScript(filepath.Join(l.Root, "lab.sh"), args...)
}

// ExecScript remplace le processus courant par un script du projet.
func (l *Lab) ExecScript(script string, args ...string) error {
	if l.Verbose {
		fmt.Fprintf(os.Stderr, "+ %s %s\n", script, strings.Join(args, " "))
	}
	return syscall.Exec(script, append([]string{script}, args...), os.Environ())
}

// Command prépare « lab.sh <args...> » sans le lancer (pour la GUI, qui capture la sortie).
func (l *Lab) Command(args ...string) *exec.Cmd {
	cmd := exec.Command(filepath.Join(l.Root, "lab.sh"), args...)
	cmd.Dir = l.Root
	if l.Verbose {
		fmt.Fprintf(os.Stderr, "+ lab.sh %s\n", strings.Join(args, " "))
	}
	return cmd
}
