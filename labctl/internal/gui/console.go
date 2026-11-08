package gui

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"labs/labctl/internal/lab"
)

// Console : commandes labctl lancées par l'interface (liste fermée), complétion, sortie au fil de l'eau.

type runRequest struct {
	Args []string `json:"args"` // [module, commande, ...], ["pki", commande, ...], ou une commande tapée dans la console
}

// consoleCmds : commandes de premier niveau que la GUI peut lancer (boutons et console), en plus des modules.
// Exclues : gui (une autre interface), gw-portal (service de la passerelle), init (crée un lab ailleurs),
// completion et l'aide (sans intérêt ici).
var consoleCmds = map[string]bool{
	"core": true, "pki": true, "status": true, "check": true, "vm": true, "net": true, "image": true,
	"lab": true, "module": true, "hostfw": true, "version": true,
}

// completion : une proposition de la console (valeur et description, comme dans le terminal).
type completion struct {
	V string `json:"v"`
	D string `json:"d,omitempty"`
}

// handleComplete complète une ligne de la console avec la complétion du terminal : « labctl __complete »
// (cobra), qui connaît modules, VMs, CA, options… Lecture seule, hors du verrou des actions. Le dernier
// argument est le mot en cours (vide après une espace). Au premier niveau, seules les commandes lançables
// depuis la GUI sont proposées.
func (s *server) handleComplete(w http.ResponseWriter, r *http.Request) {
	var req runRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<14)).Decode(&req); err != nil || len(req.Args) < 1 {
		http.Error(w, "requête invalide", http.StatusBadRequest)
		return
	}
	for _, a := range req.Args {
		if strings.ContainsAny(a, "\x00\n") {
			http.Error(w, "argument invalide", http.StatusBadRequest)
			return
		}
	}
	self, err := os.Executable()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, self, append([]string{"__complete"}, req.Args...)...)
	cmd.Dir = s.lab.Root
	out, _ := cmd.Output() // la directive finale suffit : une erreur de complétion donne une liste vide
	cur := req.Args[len(req.Args)-1]
	allowed := func(string) bool { return true }
	if len(req.Args) == 1 { // premier niveau : modules + commandes lançables (modules lus une seule fois)
		mods := map[string]bool{}
		if ms, err := s.lab.Modules(); err == nil {
			for _, m := range ms {
				mods[m.Name] = true
			}
		}
		allowed = func(v string) bool { return consoleCmds[v] || mods[v] }
	}
	items, directive := []completion{}, 0
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, ":") { // « :N » : directive cobra (2 = pas d'espace après, 4 = pas de fichiers…)
			fmt.Sscanf(line, ":%d", &directive)
			break
		}
		v, d, _ := strings.Cut(line, "\t")
		if v == "" || !strings.HasPrefix(v, cur) || !allowed(v) {
			continue
		}
		items = append(items, completion{v, d})
	}
	writeJSON(w, map[string]any{"items": items, "directive": directive})
}

// ConsoleCommands renvoie les commandes de premier niveau (hors modules) lançables depuis la GUI ; un test vérifie
// qu'elles existent toutes dans labctl.
func ConsoleCommands() []string {
	out := make([]string, 0, len(consoleCmds))
	for c := range consoleCmds {
		out = append(out, c)
	}
	sort.Strings(out)
	return out
}

// consoleAllowed dit si une commande de premier niveau peut être lancée depuis la GUI.
func (s *server) consoleAllowed(top string) bool {
	if consoleCmds[top] {
		return true
	}
	_, err := s.lab.Module(top)
	return err == nil
}

// handleRun exécute « labctl <module|core|pki> ... » et renvoie sa sortie au fil de l'eau.
func (s *server) handleRun(w http.ResponseWriter, r *http.Request) {
	var req runRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<14)).Decode(&req); err != nil || len(req.Args) < 1 {
		http.Error(w, "requête invalide", http.StatusBadRequest)
		return
	}
	if !s.consoleAllowed(req.Args[0]) {
		http.Error(w, fmt.Sprintf("« %s » ne se lance pas depuis l'interface (commande inconnue, ou réservée au terminal)", req.Args[0]), http.StatusBadRequest)
		return
	}
	// trust-host demande sudo (mot de passe dans un terminal) : uniquement en ligne de commande. Cherché partout
	// dans les arguments : une option placée avant (pki -v trust-host) ne doit pas suffire à le faire passer.
	if req.Args[0] == "pki" && slices.Contains(req.Args, "trust-host") {
		http.Error(w, "pki trust-host demande sudo : à lancer dans un terminal (labctl pki trust-host)", http.StatusBadRequest)
		return
	}
	for _, a := range req.Args {
		if strings.ContainsAny(a, "\x00\n") {
			http.Error(w, "argument invalide", http.StatusBadRequest)
			return
		}
	}
	if !s.busy.TryLock() {
		http.Error(w, "une action est déjà en cours", http.StatusConflict)
		return
	}
	defer s.busy.Unlock()
	defer s.invalidate()

	flusher, _ := w.(http.Flusher)
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	pw := &flushWriter{w: w, f: flusher}

	// Tout est en Go : on relance labctl lui-même (même analyse d'options que la CLI)
	self, err := os.Executable()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	cmd := exec.Command(self, req.Args...)
	cmd.Dir = s.lab.Root
	// La GUI demande déjà confirmation : pas de question interactive côté script
	cmd.Env = append(os.Environ(), "ASSUME_YES=1", "LABCTL_FROM_GUI=1")
	cmd.Stdout, cmd.Stderr = pw, pw
	cmd.Stdin = nil
	log.Printf("action : %s", strings.Join(req.Args, " "))
	if err := cmd.Run(); err != nil {
		fmt.Fprintf(pw, "\n[échec : %v]\n", err)
		return
	}
	fmt.Fprint(pw, "\n[terminé]\n")
}

func (s *server) invalidate() {
	s.cache.Lock()
	s.state, s.core = nil, nil
	s.cache.Unlock()
}

// handleConsole ouvre virt-viewer sur une VM connue des modules.
func (s *server) handleConsole(w http.ResponseWriter, r *http.Request) {
	var req struct {
		VM string `json:"vm"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<10)).Decode(&req); err != nil {
		http.Error(w, "requête invalide", http.StatusBadRequest)
		return
	}
	mods, err := s.lab.Modules()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	known := req.VM == lab.GatewayVM
	for _, m := range mods {
		for _, vm := range s.lab.VMs(m.Name) {
			known = known || m.LibvirtName(vm) == req.VM
		}
	}
	if !known {
		http.Error(w, "VM inconnue", http.StatusBadRequest)
		return
	}
	cmd := exec.Command("virt-viewer", "-c", "qemu:///system", req.VM)
	if err := cmd.Start(); err != nil {
		http.Error(w, "virt-viewer : "+err.Error(), http.StatusInternalServerError)
		return
	}
	go func() { _ = cmd.Wait() }()
	w.WriteHeader(http.StatusNoContent)
}

type flushWriter struct {
	mu sync.Mutex
	w  io.Writer
	f  http.Flusher
}

func (fw *flushWriter) Write(p []byte) (int, error) {
	fw.mu.Lock()
	defer fw.mu.Unlock()
	n, err := fw.w.Write(p)
	if fw.f != nil {
		fw.f.Flush()
	}
	return n, err
}
