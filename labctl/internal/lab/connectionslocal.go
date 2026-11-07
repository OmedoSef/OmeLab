package lab

import (
	"fmt"
	"strings"

	"labs/labctl/internal/module"
)

// Connexions LOCALES, non distribuées (git-ignoré) : des règles d'accès propres à CE poste, qui ne doivent pas
// voyager avec le module. Typiquement les bascules de test (portail captif, proxy log/intercept) et les accès
// d'une VM branchée localement. Elles sont fusionnées dans y.Connections au chargement (marquées Local), si bien
// que tout le reste du code (apply de la passerelle, GUI) les traite comme les connections: du module.yaml ;
// seuls l'ajout et le retrait visent ce fichier. Même rôle que attach.local.yaml pour le câblage.

// localConn : une connexion locale. « on » = module cible (dont les VMs sont la source) ; le reste est une
// connexion ordinaire (to, from, ports, via, portal, proxy), inline pour un fichier lisible.
type localConn struct {
	On                string `yaml:"on"`
	module.Connection `yaml:",inline"`
}

type localConnFile struct {
	Connections []localConn `yaml:"connections"`
}

// loadLocalConns renvoie les connexions locales (section connections de network.local.yaml, ou l'ancien
// connections.local.yaml).
func (l *Lab) loadLocalConns() ([]localConn, error) {
	f, err := l.loadNetworkLocal()
	return f.Connections, err
}

// localConnsFor renvoie les connexions locales visant le module donné.
func (l *Lab) localConnsFor(module string) []localConn {
	all, _ := l.loadLocalConns()
	var out []localConn
	for _, c := range all {
		if c.On == module {
			out = append(out, c)
		}
	}
	return out
}

// injectLocalConnections fusionne les connexions locales dans y.Connections (marquées Local), APRÈS celles du
// module.yaml pour garder les index alignés sur le fichier. À appeler sur tout *module.Module chargé.
func (l *Lab) injectLocalConnections(y *module.Module) {
	if y == nil {
		return
	}
	for _, lc := range l.localConnsFor(y.Module) {
		c := lc.Connection
		c.Local = true
		y.Connections = append(y.Connections, c)
	}
}

// saveLocalConns réécrit la section connections de network.local.yaml (le câblage local est préservé).
func (l *Lab) saveLocalConns(entries []localConn) error {
	f, err := l.loadNetworkLocal()
	if err != nil {
		return err
	}
	f.Connections = entries
	return l.saveNetworkLocal(f)
}

// AddLocalConnection ajoute une connexion locale sur le module cible, après l'avoir validée comme le ferait le
// module.yaml (une connexion refusée n'est jamais écrite).
func (l *Lab) AddLocalConnection(on string, cn module.Connection) error {
	cn.Local = false // champ interne : jamais écrit, mis à true à l'injection
	cn.To = strings.TrimSpace(cn.To)
	if err := l.validateLocalConn(on, cn); err != nil {
		return err
	}
	all, err := l.loadLocalConns()
	if err != nil {
		return err
	}
	all = append(all, localConn{On: on, Connection: cn})
	return l.saveLocalConns(all)
}

// DelLocalConnection retire la connexion locale n° i (ordre du fichier, parmi celles du module `on`).
func (l *Lab) DelLocalConnection(on string, i int) error {
	all, err := l.loadLocalConns()
	if err != nil {
		return err
	}
	n := -1
	for j := range all {
		if all[j].On == on {
			n++
			if n == i {
				all = append(all[:j], all[j+1:]...)
				return l.saveLocalConns(all)
			}
		}
	}
	return fmt.Errorf("pas de connexion locale n° %d sur %s", i, on)
}

// validateLocalConn vérifie que le module cible, muni de ses connexions locales existantes plus `extra`, passe
// le validateur (on ne garde que les erreurs de la section connections, comme pour les connexions du module.yaml).
func (l *Lab) validateLocalConn(on string, extra module.Connection) error {
	m, err := l.confModule(on)
	if err != nil {
		return err
	}
	if !m.HasYAML {
		return fmt.Errorf("%s n'a pas de %s : connexions indisponibles", on, module.FileName)
	}
	y, err := module.Load(m.Dir)
	if err != nil {
		return fmt.Errorf("%s illisible : %w", module.FileName, err)
	}
	for _, lc := range l.localConnsFor(on) {
		y.Connections = append(y.Connections, lc.Connection)
	}
	y.Connections = append(y.Connections, extra)
	var errs []string
	for _, i := range y.Validate(l.ModuleContext(on)) {
		if i.Error && strings.HasPrefix(i.Path, "connections") {
			errs = append(errs, i.Msg)
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("connexion locale refusée : %s", strings.Join(errs, " ; "))
	}
	return nil
}
