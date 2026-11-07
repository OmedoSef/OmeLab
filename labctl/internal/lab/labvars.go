package lab

import (
	"os"
	"path/filepath"

	"labs/labctl/internal/module"

	"go.yaml.in/yaml/v3"
)

// LabVars renvoie les variables de niveau lab : le bloc vars: de lab.yaml, surchargé par celui de
// lab.local.yaml (non versionné, pour les secrets et les réglages propres au poste). Ces variables
// surchargent le vars: de chaque module : ${var.X} d'un module prend la valeur du lab si elle existe,
// sinon le défaut du module. C'est le point d'entrée pour « modifier 2/3 trucs » (domaine, mot de passe,
// chemin des images...) en un seul endroit, sans toucher aux modules.
//
// Résolu une seule fois par lab (les ${...} internes au bloc sont développés). Un lab sans lab.yaml
// renvoie une map vide : les modules gardent alors le comportement d'avant (défauts du module).
func (l *Lab) LabVars() map[string]string {
	if !l.labVarsDone {
		l.labVarsCache = l.loadLabVars()
		l.labVarsDone = true
	}
	return l.labVarsCache
}

func (l *Lab) loadLabVars() map[string]string {
	vars := map[string]string{}
	for _, name := range []string{"lab.yaml", "lab.local.yaml"} { // lab.local.yaml a le dernier mot
		b, err := os.ReadFile(filepath.Join(l.Root, name))
		if err != nil {
			continue
		}
		var doc struct {
			Vars map[string]string `yaml:"vars"`
		}
		if err := yaml.Unmarshal(b, &doc); err != nil {
			continue // un lab.yaml illisible ne doit pas bloquer tout labctl ; les ${var.X} échoueront plus loin, clairement
		}
		for k, v := range doc.Vars {
			vars[k] = v
		}
	}
	// Au mieux : si la résolution échoue (cycle, var inconnue), on garde les valeurs telles quelles et
	// l'erreur remontera à l'expansion du module qui s'en sert, avec son nom.
	_ = module.ResolveLabVars(vars, l.Keyboard())
	return vars
}
