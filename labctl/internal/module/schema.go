package module

import _ "embed"

// SchemaJSON : schéma JSON (draft-07) de module.yaml pour les éditeurs (VS Code et tout éditeur basé sur
// yaml-language-server) : complétion des clés, description au survol, fautes de frappe signalées. Il est généré
// depuis les structures de ce paquet et leurs commentaires — il ne peut donc pas diverger du code : un test échoue
// s'il n'est plus à jour (régénérer : go test ./internal/module -run TestSchemaUpToDate -update).
//
//go:embed module.schema.json
var SchemaJSON []byte

const (
	// SchemaFile : nom du schéma posé à la racine d'un lab (labctl init, labctl module schema).
	SchemaFile = "module.schema.json"
	// SchemaModeline : première ligne d'un module.yaml, qui désigne ce schéma à l'éditeur. Un module est toujours
	// dans <lab>/modules/<nom>/, d'où le chemin relatif. Elle passe avant les schémas du catalogue SchemaStore (dont
	// celui d'un autre outil, cfgd, qui revendique aussi les fichiers nommés module.yaml).
	SchemaModeline = "# yaml-language-server: $schema=../../" + SchemaFile
)
