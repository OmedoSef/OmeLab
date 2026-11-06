package lab

import (
	"fmt"
	"strings"

	mod "labs/labctl/internal/module"
)

// Connexions d'un module (connections: du module.yaml), modifiées depuis la GUI. Le module modifié est
// validé en entier avant d'être enregistré : une connexion refusée par le validateur n'est jamais écrite.

// ConfAddConnection ajoute une connexion au module.
func (l *Lab) ConfAddConnection(module string, cn mod.Connection) error {
	return l.editConnections(module, func(y *mod.Module) error {
		cn.To = strings.TrimSpace(cn.To)
		y.AddConnection(cn)
		return nil
	})
}

// ConfDelConnection retire la connexion n° i (ordre du fichier).
func (l *Lab) ConfDelConnection(module string, i int) error {
	return l.editConnections(module, func(y *mod.Module) error {
		if !y.DelConnection(i) {
			return fmt.Errorf("pas de connexion n° %d dans %s", i, module)
		}
		return nil
	})
}

func (l *Lab) editConnections(module string, edit func(*mod.Module) error) error {
	m, err := l.confModule(module)
	if err != nil {
		return err
	}
	if !m.HasYAML {
		return fmt.Errorf("%s n'a pas de %s : connexions indisponibles", module, mod.FileName)
	}
	y, err := mod.Load(m.Dir)
	if err != nil {
		return fmt.Errorf("%s illisible : %w", mod.FileName, err)
	}
	if err := edit(y); err != nil {
		return err
	}
	b, err := y.Bytes()
	if err != nil {
		return err
	}
	check, err := mod.Parse(b, m.Dir)
	if err != nil {
		return err
	}
	var errs []string
	for _, i := range check.Validate(l.ModuleContext(module)) {
		if i.Error && strings.HasPrefix(i.Path, "connections") {
			errs = append(errs, i.Msg)
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("connexion refusée : %s", strings.Join(errs, " ; "))
	}
	return y.Save()
}
