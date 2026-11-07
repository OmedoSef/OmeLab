package lab

// EngineCommands : commandes du moteur des modules (labctl <module> …), utilisées par la GUI pour savoir quels
// boutons proposer. La définition complète (arguments, aide) est dans cli/moduleengine.go.
var EngineCommands = []string{"check", "up", "status", "start", "stop", "snapshot", "restore", "certs", "keyboard",
	"ssh", "destroy", "show", "validate"}
