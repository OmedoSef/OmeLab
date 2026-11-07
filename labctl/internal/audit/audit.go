// Package audit tient le journal des actions de labctl : qui a lancé quoi, quand, depuis où (ligne de commande ou
// interface), sur quel lab, avec quel résultat. Un fichier JSONL (une entrée par ligne) par utilisateur, lisible par
// lui seul (0600), hors du dépôt : $XDG_STATE_HOME/labctl/audit.jsonl (défaut ~/.local/state/labctl/audit.jsonl).
// LABCTL_AUDIT choisit un autre fichier, ou « off » pour ne rien journaliser. Au-delà de 5 Mo, le fichier devient
// audit.jsonl.1 (une génération gardée). Le journal ne bloque jamais une action : une écriture impossible est ignorée.
package audit

import (
	"encoding/json"
	"os"
	"os/user"
	"path/filepath"
	"sync"
	"time"
)

// Entry : une entrée du journal.
type Entry struct {
	Time     string   `json:"time"`
	Event    string   `json:"event"`  // start / end (commande labctl), gui (action faite par l'interface elle-même)
	Origin   string   `json:"origin"` // cli, ou gui (commande lancée par l'interface, ou action de l'interface)
	User     string   `json:"user"`
	PID      int      `json:"pid"`
	Lab      string   `json:"lab,omitempty"`
	Args     []string `json:"args,omitempty"`   // arguments de la commande, ou paramètres de l'action
	Action   string   `json:"action,omitempty"` // gui : route de l'interface
	OK       *bool    `json:"ok,omitempty"`
	Error    string   `json:"error,omitempty"`
	Duration int64    `json:"duration_ms,omitempty"`
}

const maxSize = 5 << 20

var mu sync.Mutex

// Path renvoie le fichier du journal ("" : désactivé).
func Path() string {
	if p := os.Getenv("LABCTL_AUDIT"); p != "" {
		if p == "off" {
			return ""
		}
		return p
	}
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		base = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(base, "labctl", "audit.jsonl")
}

// Origin : « gui » pour labctl lancé par l'interface (LABCTL_FROM_GUI), « cli » sinon.
func Origin() string {
	if os.Getenv("LABCTL_FROM_GUI") != "" {
		return "gui"
	}
	return "cli"
}

// Log ajoute une entrée (heure, utilisateur et PID complétés ici).
func Log(e Entry) {
	p := Path()
	if p == "" {
		return
	}
	e.Time = time.Now().Format("2006-01-02T15:04:05.000Z07:00")
	if e.User == "" {
		if u, err := user.Current(); err == nil {
			e.User = u.Username
		}
	}
	e.PID = os.Getpid()
	b, err := json.Marshal(e)
	if err != nil {
		return
	}
	mu.Lock()
	defer mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return
	}
	if fi, err := os.Stat(p); err == nil && fi.Size() > maxSize {
		_ = os.Rename(p, p+".1")
	}
	f, err := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.Write(append(b, '\n')) // une ligne par écriture en O_APPEND : pas d'entrelacement entre processus
}
