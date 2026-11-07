package lab

import (
	"bytes"
	"os"
	"path/filepath"

	"go.yaml.in/yaml/v3"
)

// network.local.yaml réunit le câblage et les connexions propres à CE poste en un seul fichier à la racine du
// lab (git-ignoré, non distribué). Deux sections, toutes deux indexées par « on » = module cible : attach (VMs
// d'autres modules branchées sur un module) et connections (règles d'accès locales). Il remplace les anciens
// attach.local.yaml / connections.local.yaml, encore lus s'ils existent et migrés ici dès le premier
// enregistrement. attachlocal.go et connectionslocal.go gardent leur logique ; seule la persistance passe ici.

type networkLocalFile struct {
	Attach      []localAttach `yaml:"attach,omitempty"`
	Connections []localConn   `yaml:"connections,omitempty"`
}

func (l *Lab) networkLocalPath() string { return filepath.Join(l.Root, "network.local.yaml") }
func (l *Lab) legacyAttachPath() string { return filepath.Join(l.Root, "attach.local.yaml") }
func (l *Lab) legacyConnPath() string   { return filepath.Join(l.Root, "connections.local.yaml") }

// loadNetworkLocal lit network.local.yaml ; à défaut, retombe sur les anciens fichiers séparés (compat).
func (l *Lab) loadNetworkLocal() (networkLocalFile, error) {
	var f networkLocalFile
	if b, err := os.ReadFile(l.networkLocalPath()); err == nil {
		return f, yaml.Unmarshal(b, &f)
	} else if !os.IsNotExist(err) {
		return f, err
	}
	// Compat : anciens fichiers séparés, tant que network.local.yaml n'a pas été écrit.
	if b, err := os.ReadFile(l.legacyAttachPath()); err == nil {
		var a localAttachFile
		if err := yaml.Unmarshal(b, &a); err != nil {
			return f, err
		}
		f.Attach = a.Attach
	}
	if b, err := os.ReadFile(l.legacyConnPath()); err == nil {
		var c localConnFile
		if err := yaml.Unmarshal(b, &c); err != nil {
			return f, err
		}
		f.Connections = c.Connections
	}
	return f, nil
}

const networkLocalHeader = `# Câblage et connexions LOCAUX, non distribués (git-ignoré) : propres à CE poste.
# Édités par la GUI : onglet VMs d'un module (Brancher / Débrancher), onglet Réseau → Connexions (case « local à
# ce poste »). « on » = module cible. attach: VMs d'autres modules branchées ici ; connections: accès locaux.
`

// saveNetworkLocal réécrit network.local.yaml (supprimé si les deux sections sont vides), puis retire les anciens
// fichiers séparés : la migration vers le fichier unique n'est actée qu'une fois l'écriture réussie, pour ne
// jamais perdre le câblage si elle échoue. Écriture atomique (fichier temporaire puis renommage).
func (l *Lab) saveNetworkLocal(f networkLocalFile) error {
	if len(f.Attach) == 0 && len(f.Connections) == 0 {
		if err := os.Remove(l.networkLocalPath()); err != nil && !os.IsNotExist(err) {
			return err
		}
	} else {
		b, err := yaml.Marshal(f)
		if err != nil {
			return err
		}
		data := append([]byte(networkLocalHeader), b...)
		if err := writeAtomic(l.networkLocalPath(), bytes.NewReader(data), int64(len(data))); err != nil {
			return err
		}
	}
	for _, old := range []string{l.legacyAttachPath(), l.legacyConnPath()} {
		if err := os.Remove(old); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}
