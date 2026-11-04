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
	"strings"
	"sync"
	"syscall"
	"time"

	"labs/labctl/internal/lab"
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
	mux.Handle("GET /", http.FileServerFS(sub))
	mux.HandleFunc("GET /api/state", s.api(s.handleState))
	mux.HandleFunc("POST /api/run", s.api(s.handleRun))
	mux.HandleFunc("POST /api/console", s.api(s.handleConsole))

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

type runRequest struct {
	Args []string `json:"args"` // [module, commande, ...]
}

// handleRun exécute « lab.sh <module> ... » et renvoie sa sortie au fil de l'eau.
func (s *server) handleRun(w http.ResponseWriter, r *http.Request) {
	var req runRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<14)).Decode(&req); err != nil || len(req.Args) < 2 {
		http.Error(w, "requête invalide", http.StatusBadRequest)
		return
	}
	if _, err := s.lab.Module(req.Args[0]); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
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

	cmd := s.lab.Command(req.Args...)
	// La GUI demande déjà confirmation : pas de question interactive côté script
	cmd.Env = append(os.Environ(), "ASSUME_YES=1")
	cmd.Stdout, cmd.Stderr = pw, pw
	cmd.Stdin = nil
	log.Printf("action : lab.sh %s", strings.Join(req.Args, " "))
	if err := cmd.Run(); err != nil {
		fmt.Fprintf(pw, "\n[échec : %v]\n", err)
		return
	}
	fmt.Fprint(pw, "\n[terminé]\n")
}

func (s *server) invalidate() {
	s.cache.Lock()
	s.state = nil
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
