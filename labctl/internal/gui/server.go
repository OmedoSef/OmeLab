// Package gui sert l'interface web locale de labctl.
//
// Sécurité : écoute sur 127.0.0.1 seulement ; chaque appel d'API exige le jeton aléatoire tiré au
// lancement (transmis dans le fragment #t=... de l'URL, jamais envoyé au serveur ni journalisé) ;
// l'en-tête Host est vérifié (protection contre le DNS rebinding) ; une seule action à la fois.
package gui

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"labs/labctl/internal/lab"
	"labs/labctl/internal/module"
)

//go:embed static
var static embed.FS

type server struct {
	lab   *lab.Lab
	token string
	host  string // valeur attendue de l'en-tête Host

	busy   sync.Mutex // une action à la fois
	cache  sync.Mutex
	state  *lab.State
	stated time.Time
	core   *lab.CoreState
	cored  time.Time
}

// Serve lance l'interface sur 127.0.0.1:port et ouvre le navigateur si demandé.
func Serve(l *lab.Lab, port int, openBrowser bool) error {
	tok := make([]byte, 24)
	if _, err := rand.Read(tok); err != nil {
		return err
	}
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return fmt.Errorf("port %d indisponible (labctl gui -p AUTRE_PORT) : %w", port, err)
	}
	addr := ln.Addr().(*net.TCPAddr)
	s := &server{lab: l, token: hex.EncodeToString(tok), host: fmt.Sprintf("127.0.0.1:%d", addr.Port)}

	mux := http.NewServeMux()
	sub, _ := fs.Sub(static, "static")
	// La page est embarquée (sans date) : sans no-cache, le navigateur garde l'ancienne après une mise à jour de labctl
	files := http.FileServerFS(sub)
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")
		files.ServeHTTP(w, r)
	})
	mux.HandleFunc("GET /api/state", s.api(s.handleState))
	mux.HandleFunc("POST /api/run", s.api(s.handleRun))
	mux.HandleFunc("POST /api/console", s.api(s.handleConsole))
	mux.HandleFunc("GET /api/core", s.api(s.handleCore))
	mux.HandleFunc("GET /api/pki", s.api(s.handlePKI))
	mux.HandleFunc("GET /api/pki/file", s.api(s.handlePKIFile))
	mux.HandleFunc("GET /api/config", s.api(s.handleConfig))
	mux.HandleFunc("POST /api/config", s.api(s.handleConfigEdit))
	mux.HandleFunc("GET /api/modules", s.api(s.handleModules))
	mux.HandleFunc("GET /api/module", s.api(s.handleModuleGet))
	mux.HandleFunc("GET /api/module/skeleton", s.api(s.handleModuleSkeleton))
	mux.HandleFunc("POST /api/module/check", s.api(s.handleModuleCheck))
	mux.HandleFunc("POST /api/module/save", s.api(s.handleModuleSave))

	srv := &http.Server{Handler: s.checkHost(mux), ReadHeaderTimeout: 10 * time.Second}
	url := fmt.Sprintf("http://%s/#t=%s", s.host, s.token)
	fmt.Printf("Interface : %s\n(Ctrl+C pour arrêter ; l'adresse contient le jeton d'accès, ne la partage pas)\n", url)
	if openBrowser {
		if err := exec.Command("xdg-open", url).Start(); err != nil {
			fmt.Fprintln(os.Stderr, "navigateur non ouvert :", err)
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shut, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = srv.Shutdown(shut)
	}()
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// checkHost refuse les requêtes dont l'en-tête Host n'est pas notre adresse locale (DNS rebinding).
func (s *server) checkHost(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != s.host && r.Host != strings.Replace(s.host, "127.0.0.1", "localhost", 1) {
			http.Error(w, "hôte refusé", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// api exige le jeton dans l'en-tête X-Lab-Token (un en-tête personnalisé bloque aussi le CSRF).
func (s *server) api(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Lab-Token")), []byte(s.token)) != 1 {
			http.Error(w, "jeton invalide : rouvrir l'adresse affichée par labctl gui", http.StatusUnauthorized)
			return
		}
		h(w, r)
	}
}

func (s *server) handleState(w http.ResponseWriter, r *http.Request) {
	// L'état interroge libvirt et la passerelle (SSH) : on le garde 2 s pour ne pas surcharger
	s.cache.Lock()
	defer s.cache.Unlock()
	if s.state == nil || time.Since(s.stated) > 2*time.Second || r.URL.Query().Has("fresh") {
		st, err := s.lab.Status()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		s.state, s.stated = st, time.Now()
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(s.state)
}

func (s *server) handleCore(w http.ResponseWriter, r *http.Request) {
	// Une connexion SSH à la passerelle : gardée 3 s
	s.cache.Lock()
	if s.core == nil || time.Since(s.cored) > 3*time.Second || r.URL.Query().Has("fresh") {
		s.core, s.cored = s.lab.Core(), time.Now()
	}
	cs := s.core
	s.cache.Unlock()
	writeJSON(w, cs)
}

func (s *server) handlePKI(w http.ResponseWriter, r *http.Request) {
	st, err := s.lab.PKI()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"pki": st, "vms": s.lab.AllVMNames()})
}

// handlePKIFile sert un certificat public de la PKI (jamais une clé privée ni un .p12).
func (s *server) handlePKIFile(w http.ResponseWriter, r *http.Request) {
	p, err := s.lab.PublicFile(r.URL.Query().Get("path"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	w.Header().Set("Content-Type", "application/x-pem-file")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filepath.Base(p)))
	http.ServeFile(w, r, p)
}

// Configuration éditable (section dns du module.yaml) de chaque module, et changements en attente sur la passerelle.
type configHost struct {
	Name   string `json:"name"`
	IP     string `json:"ip"`
	MAC    string `json:"mac"`
	Source string `json:"source"` // vm (vms: du module, lecture seule ici) ou reservation (dns:)
}

type configModule struct {
	Name    string            `json:"name"`
	Subnet  string            `json:"subnet"`
	Domain  string            `json:"domain"`
	Hosts   []configHost      `json:"hosts"`
	Aliases map[string]string `json:"aliases"`
	Links   []string          `json:"links"`
}

func (s *server) handleConfig(w http.ResponseWriter, r *http.Request) {
	mods, err := s.lab.Modules()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	var out []configModule
	for _, m := range mods {
		if m.Env == nil {
			continue
		}
		cm := configModule{Name: m.Name, Subnet: m.Env.Subnet, Domain: m.Env.Domain, Aliases: m.Conf.Aliases, Links: m.Conf.Links}
		for _, vm := range m.Env.VMs() {
			cm.Hosts = append(cm.Hosts, configHost{vm, m.Env.IP(vm), m.Env.Macs[vm], "vm"})
		}
		for h, o := range m.Conf.Hosts {
			cm.Hosts = append(cm.Hosts, configHost{h, fmt.Sprintf("%s.%d", m.Env.Subnet, o), m.Conf.HostMACs[h], "reservation"})
		}
		out = append(out, cm)
	}
	resp := map[string]any{"modules": out, "gateway": lab.DomState(lab.GatewayVM)}
	if resp["gateway"] == "running" {
		if changes, err := s.lab.PendingCoreChanges(); err != nil {
			resp["pendingError"] = err.Error()
		} else {
			resp["pending"] = changes
		}
	}
	writeJSON(w, resp)
}

type configEdit struct {
	Op     string `json:"op"` // host-add, host-del, alias-add, alias-del, link, unlink
	Module string `json:"module"`
	Name   string `json:"name"`
	Octet  int    `json:"octet"`
	MAC    string `json:"mac"`
	Target string `json:"target"`
}

// handleConfigEdit modifie la section dns du module.yaml (sans appliquer : voir « Appliquer » dans l'onglet Configuration).
func (s *server) handleConfigEdit(w http.ResponseWriter, r *http.Request) {
	var e configEdit
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<12)).Decode(&e); err != nil {
		http.Error(w, "requête invalide", http.StatusBadRequest)
		return
	}
	if !s.busy.TryLock() {
		http.Error(w, "une action est en cours", http.StatusConflict)
		return
	}
	defer s.busy.Unlock()
	var err error
	switch e.Op {
	case "host-add":
		err = s.lab.ConfAddHost(e.Module, e.Name, e.Octet, e.MAC)
	case "host-del":
		err = s.lab.ConfDelHost(e.Module, e.Name)
	case "alias-add":
		err = s.lab.ConfAddAlias(e.Module, e.Name, e.Target)
	case "alias-del":
		err = s.lab.ConfDelAlias(e.Module, e.Name)
	case "link", "unlink":
		err = s.lab.ConfLink(e.Module, e.Target, e.Op == "link")
	default:
		err = fmt.Errorf("opération inconnue : %s", e.Op)
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	log.Printf("configuration : %s %s %s %s", e.Op, e.Module, e.Name, e.Target)
	s.invalidate()
	w.WriteHeader(http.StatusNoContent)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

type runRequest struct {
	Args []string `json:"args"` // [module, commande, ...] ou ["pki", commande, ...]
}

// handleRun exécute « labctl <module|core|pki> ... » et renvoie sa sortie au fil de l'eau.
func (s *server) handleRun(w http.ResponseWriter, r *http.Request) {
	var req runRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<14)).Decode(&req); err != nil || len(req.Args) < 2 {
		http.Error(w, "requête invalide", http.StatusBadRequest)
		return
	}
	if req.Args[0] == "pki" {
		// trust-host demande sudo (mot de passe dans un terminal) : uniquement en ligne de commande
		if req.Args[1] == "trust-host" {
			http.Error(w, "pki trust-host demande sudo : à lancer dans un terminal (labctl pki trust-host)", http.StatusBadRequest)
			return
		}
	} else if req.Args[0] != "core" {
		if _, err := s.lab.Module(req.Args[0]); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
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
	cmd.Env = append(os.Environ(), "ASSUME_YES=1")
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

// ---------------------------------------------------------------- éditeur de module.yaml

// issue est un problème de validation tel que l'affiche l'éditeur.
type issue struct {
	Error bool   `json:"error"`
	Path  string `json:"path"`
	Line  int    `json:"line"`
	Msg   string `json:"msg"`
}

func issuesJSON(in []module.Issue) []issue {
	out := []issue{}
	for _, i := range in {
		out = append(out, issue{i.Error, i.Path, i.Line, i.Msg})
	}
	return out
}

// handleModules liste les modules déclaratifs (module.yaml).
func (s *server) handleModules(w http.ResponseWriter, _ *http.Request) {
	mods, err := s.lab.Modules()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	names := []string{}
	for _, m := range mods {
		if m.HasYAML {
			names = append(names, m.Name)
		}
	}
	writeJSON(w, map[string]any{"modules": names, "reserved": module.ReservedNames})
}

func (s *server) handleModuleGet(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("name")
	text, hash, err := s.lab.ModuleText(name)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	writeJSON(w, map[string]any{"name": name, "text": text, "hash": hash, "issues": issuesJSON(s.lab.CheckModuleText(name, text))})
}

func (s *server) handleModuleSkeleton(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("name")
	text := lab.ModuleSkeleton(name)
	writeJSON(w, map[string]any{"name": name, "text": text, "issues": issuesJSON(s.lab.CheckModuleText(name, text))})
}

type moduleText struct {
	Name   string `json:"name"`
	Text   string `json:"text"`
	Hash   string `json:"hash"`   // empreinte du fichier lu ("" pour un nouveau module)
	Create bool   `json:"create"` // nouveau module
}

func decodeModuleText(w http.ResponseWriter, r *http.Request) (moduleText, bool) {
	var req moduleText
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil || req.Name == "" {
		http.Error(w, "requête invalide", http.StatusBadRequest)
		return req, false
	}
	return req, true
}

// handleModuleCheck valide un texte sans l'enregistrer (validation à la volée).
func (s *server) handleModuleCheck(w http.ResponseWriter, r *http.Request) {
	req, ok := decodeModuleText(w, r)
	if !ok {
		return
	}
	writeJSON(w, map[string]any{"issues": issuesJSON(s.lab.CheckModuleText(req.Name, req.Text))})
}

// handleModuleSave enregistre un texte valide (refusé s'il contient des erreurs ou si le fichier a changé).
func (s *server) handleModuleSave(w http.ResponseWriter, r *http.Request) {
	req, ok := decodeModuleText(w, r)
	if !ok {
		return
	}
	if !s.busy.TryLock() {
		http.Error(w, "une action est en cours", http.StatusConflict)
		return
	}
	defer s.busy.Unlock()
	hash, issues, err := s.lab.SaveModuleText(req.Name, req.Text, req.Hash, req.Create)
	resp := map[string]any{"hash": hash, "issues": issuesJSON(issues)}
	if err != nil {
		code := http.StatusBadRequest
		if errors.Is(err, lab.ErrConflict) {
			code = http.StatusConflict
		}
		resp["error"] = err.Error()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(resp)
		return
	}
	log.Printf("module.yaml enregistré : %s", req.Name)
	s.invalidate()
	writeJSON(w, resp)
}
