// Package lab donne accès aux labs : découverte des modules (modules/<nom>/module.yaml, la source de vérité),
// réglages du lab, moteur des modules (réseaux, VMs, certificats), passerelle core-gw et fichiers propres au poste
// (lab.local.yaml, network.local.yaml). Les structures Env / CoreConf sont une vue dérivée du module.yaml, héritée
// de l'ancien format bash (module.env, core.conf), qui n'est plus lu.
package lab

import (
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

	"labs/labctl/internal/module"
	"labs/labctl/internal/virt"
)

// Lab est la racine d'une arborescence Labs (dossier contenant config.env et modules/).
type Lab struct {
	Root    string
	Verbose bool // affiche sur stderr les commandes lancées
	// Virt est l'hyperviseur ; nil = libvirt (connexion commune au processus). Les tests y mettent un faux
	// (virt/virtfake).
	Virt virt.Hypervisor

	labVarsCache map[string]string // vars: du lab.yaml, résolues (voir LabVars)
	labVarsDone  bool
}

// Hypervisor renvoie l'hyperviseur du lab : libvirt, sauf faux injecté par un test (champ Virt).
func (l *Lab) Hypervisor() virt.Hypervisor {
	if l.Virt != nil {
		return l.Virt
	}
	return virt.Shared()
}

// Find localise la racine : $LABS_ROOT (posé par activate), sinon en remontant depuis l'exécutable,
// sinon depuis le dossier courant.
func Find() (*Lab, error) {
	if r := os.Getenv("LABS_ROOT"); r != "" {
		if isRoot(r) {
			return &Lab{Root: r}, nil
		}
		return nil, fmt.Errorf("LABS_ROOT=%s n'est pas une racine Labs (config.env et modules/ attendus)", r)
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
	_, err = os.Stat(filepath.Join(d, "config.env"))
	return err == nil
}

// Module décrit un module : un dossier modules/<nom>/ avec un module.yaml.
type Module struct {
	Name        string
	Dir         string
	Description string    // description: du module.yaml
	Env         *Env      // vue réseau dérivée du module.yaml ; nil tant que le module n'a pas de sous-réseau
	Commands    []string  // commandes du moteur (labctl <module> …)
	Conf        *CoreConf // réservations, alias et liens dérivés du module.yaml (vide par défaut)

	// Module déclaratif : module.yaml (source de vérité ; Env et Conf en sont dérivés)
	HasYAML bool
	YAML    *module.Module
	YAMLErr error
}

// Env : vue réseau d'un module (nom du réseau, préfixe, domaine, VMs à IP fixe), dérivée de son module.yaml.
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

// CoreConf : réservations en plus, alias DNS, liens, branchements (dns: et attach: du module.yaml).
type CoreConf struct {
	Hosts    map[string]int    // réservations en plus : nom -> dernier octet
	HostMACs map[string]string // réservations en plus : nom -> MAC
	Aliases  map[string]string
	Links    []string
	Attached map[string]string // VMs d'autres modules branchées ici (attach:) : nom -> <module>/<vm>
}

// LibvirtName donne le nom libvirt d'une VM de module (convention <module>-<vm>).
func (m *Module) LibvirtName(vm string) string { return m.Name + "-" + vm }

// Modules liste les modules, triés par nom.
func (l *Lab) Modules() ([]*Module, error) {
	entries, err := os.ReadDir(filepath.Join(l.Root, "modules"))
	if err != nil {
		return nil, err
	}
	var mods []*Module
	for _, e := range entries {
		dir := filepath.Join(l.Root, "modules", e.Name())
		if _, err := os.Stat(filepath.Join(dir, module.FileName)); err != nil {
			continue
		}
		m := &Module{Name: e.Name(), Dir: dir, Commands: EngineCommands, Conf: newCoreConf()}
		l.loadYAML(m)
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

// Identité par défaut d'un lab : organisation des CA et domaine racine. Réglables par lab dans config.env
// (LABS_PKI_ORG, LABS_PKI_DOMAIN), choisis au « labctl init ». .internal est réservé aux réseaux privés.
const (
	DefaultOrg    = "OmeLab"
	DefaultDomain = "omelab.internal"
)

// Org renvoie l'organisation du lab (nom des CA, du propriétaire des VMs Windows…) : LABS_PKI_ORG.
func (l *Lab) Org() string { return l.Setting("LABS_PKI_ORG", DefaultOrg) }

// RootDomain renvoie le domaine racine des labs (LABS_PKI_DOMAIN).
func (l *Lab) RootDomain() string { return l.Setting("LABS_PKI_DOMAIN", DefaultDomain) }

// Vulnerable dit si le module est marqué vulnerable: true (cibles volontairement vulnérables, voir module.yaml).
func (m *Module) Vulnerable() bool { return m != nil && m.YAML != nil && m.YAML.Vulnerable }

// pkiDomain : ancien nom interne de RootDomain.
func (l *Lab) pkiDomain() string { return l.RootDomain() }

// newCoreConf : configuration réseau vide (réservations, alias, liens), complétée depuis le module.yaml.
func newCoreConf() *CoreConf {
	return &CoreConf{Hosts: map[string]int{}, HostMACs: map[string]string{}, Aliases: map[string]string{}}
}

// ExecScript remplace le processus courant par un script du projet.
func (l *Lab) ExecScript(script string, args ...string) error {
	if l.Verbose {
		fmt.Fprintf(os.Stderr, "+ %s %s\n", script, strings.Join(args, " "))
	}
	return syscall.Exec(script, append([]string{script}, args...), os.Environ())
}

// onInterrupt exécute cleanup si labctl reçoit SIGINT ou SIGTERM avant l'appel de la fonction renvoyée, puis
// termine le processus (code 128 + signal). Pour défaire un état risqué laissé sur la passerelle.
func onInterrupt(cleanup func()) (stop func()) {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
	done := make(chan struct{})
	go func() {
		select {
		case sig := <-ch:
			cleanup()
			code := 130
			if s, ok := sig.(syscall.Signal); ok {
				code = 128 + int(s)
			}
			os.Exit(code)
		case <-done:
		}
	}()
	return func() { signal.Stop(ch); close(done) }
}
