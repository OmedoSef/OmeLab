// Package gui sert l'interface web locale de labctl.
//
// Sécurité : écoute sur 127.0.0.1 seulement ; chaque appel d'API exige le jeton aléatoire tiré au
// lancement (transmis dans le fragment #t=... de l'URL, jamais envoyé au serveur ni journalisé) ;
// l'en-tête Host est vérifié (protection contre le DNS rebinding) ; une seule action à la fois.
package gui

import (
	"bytes"
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
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"labs/labctl/internal/audit"
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
	core   *lab.CoreState
	cored  time.Time

	tunnel  sync.Mutex // tunnel SSH vers mitmweb, ouvert à la demande, fermé avec l'interface
	tunAddr string
	tunCmd  *exec.Cmd
	tunDone chan struct{} // fermé quand le tunnel s'arrête
}

// csp : politique de sécurité de la page de la GUI (posée par routes).
const csp = "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; " +
	"connect-src 'self'; object-src 'none'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"

// Serve lance l'interface sur 127.0.0.1:port et ouvre le navigateur si demandé.
func Serve(l *lab.Lab, port int, open Open) error {
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

	srv := &http.Server{Handler: s.routes(), ReadHeaderTimeout: 10 * time.Second}
	url := fmt.Sprintf("http://%s/#t=%s", s.host, s.token)
	fmt.Printf("Interface : %s\n(Ctrl+C pour arrêter ; l'adresse contient le jeton d'accès, ne la partage pas)\n", url)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	switch open {
	case OpenBrowser:
		if err := exec.Command("xdg-open", url).Start(); err != nil {
			fmt.Fprintln(os.Stderr, "navigateur non ouvert :", err)
		}
	case OpenApp:
		done, err := startApp(url)
		if err != nil {
			fmt.Fprintln(os.Stderr, "fenêtre non ouverte :", err, "; ouvrir l'adresse ci-dessus dans un navigateur")
			break
		}
		go func() { <-done; fmt.Println("fenêtre fermée : arrêt de l'interface"); stop() }()
	}
	go func() {
		<-ctx.Done()
		s.tunnel.Lock()
		if s.tunCmd != nil {
			_ = s.tunCmd.Process.Kill()
		}
		s.tunnel.Unlock()
		shut, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = srv.Shutdown(shut)
	}()
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// routes renvoie l'interface complète : page embarquée et API, derrière le contrôle de l'en-tête Host. Chaque route
// de l'API exige le jeton (api) ; celles qui modifient quelque chose sans relancer labctl sont journalisées (audited).
func (s *server) routes() http.Handler {
	mux := http.NewServeMux()
	sub, _ := fs.Sub(static, "static")
	// La page est embarquée (sans date) : sans no-cache, le navigateur garde l'ancienne après une mise à jour de labctl.
	// Elle n'a aucun script en ligne : la CSP n'autorise que ses propres fichiers, ce qui neutralise un script injecté
	// par une donnée mal échappée (nom de module, sortie de commande…). Les styles en ligne (attributs style=) restent permis.
	files := http.FileServerFS(sub)
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Content-Security-Policy", csp)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		files.ServeHTTP(w, r)
	})
	mux.HandleFunc("GET /api/state", s.api(s.handleState))
	mux.HandleFunc("POST /api/run", s.api(s.handleRun))
	mux.HandleFunc("POST /api/complete", s.api(s.handleComplete))
	mux.HandleFunc("POST /api/console", s.api(s.audited(s.handleConsole)))
	mux.HandleFunc("GET /api/core", s.api(s.handleCore))
	mux.HandleFunc("GET /api/pki", s.api(s.handlePKI))
	mux.HandleFunc("GET /api/pki/trust", s.api(s.handlePKITrust))
	mux.HandleFunc("GET /api/pki/file", s.api(s.handlePKIFile))
	mux.HandleFunc("GET /api/config", s.api(s.handleConfig))
	mux.HandleFunc("POST /api/config", s.api(s.audited(s.handleConfigEdit)))
	mux.HandleFunc("GET /api/modules", s.api(s.handleModules))
	mux.HandleFunc("GET /api/module", s.api(s.handleModuleGet))
	mux.HandleFunc("GET /api/module/skeleton", s.api(s.handleModuleSkeleton))
	mux.HandleFunc("POST /api/module/check", s.api(s.handleModuleCheck))
	mux.HandleFunc("POST /api/module/save", s.api(s.audited(s.handleModuleSave)))
	mux.HandleFunc("GET /api/proxy/log", s.api(s.handleProxyLog))
	mux.HandleFunc("POST /api/proxy/web", s.api(s.audited(s.handleProxyWeb)))
	mux.HandleFunc("GET /api/files", s.api(s.handleFiles))
	mux.HandleFunc("GET /api/file", s.api(s.handleFileText))
	mux.HandleFunc("GET /api/file/raw", s.api(s.handleFileRaw))
	mux.HandleFunc("POST /api/file/save", s.api(s.audited(s.handleFileSave)))
	mux.HandleFunc("POST /api/file/upload", s.api(s.audited(s.handleFileUpload)))
	mux.HandleFunc("POST /api/file/op", s.api(s.audited(s.handleFileOp)))

	return s.checkHost(mux)
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

// statusRecorder retient le code de réponse, pour le journal d'audit.
type statusRecorder struct {
	http.ResponseWriter
	code int
}

func (r *statusRecorder) WriteHeader(c int) { r.code = c; r.ResponseWriter.WriteHeader(c) }

// audited journalise une action faite par l'interface elle-même, sans relancer labctl (qui, lui, se journalise
// seul) : route, paramètres — sans contenu de fichier ni mot de passe — et résultat.
func (s *server) audited(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var params []string
		if q := r.URL.RawQuery; q != "" { // envoi de fichier : module et chemin dans l'adresse
			params = []string{q}
		}
		if ct := r.Header.Get("Content-Type"); ct == "" || strings.HasPrefix(ct, "application/json") {
			// Paramètres relus dans les 8 premiers Mio ; le gestionnaire reçoit le corps entier (début relu + suite)
			head, _ := io.ReadAll(io.LimitReader(r.Body, 8<<20))
			r.Body = struct {
				io.Reader
				io.Closer
			}{io.MultiReader(bytes.NewReader(head), r.Body), r.Body}
			var m map[string]any
			if json.Unmarshal(head, &m) == nil {
				params = append(params, auditParams("", m)...)
			}
		}
		rec := &statusRecorder{ResponseWriter: w, code: http.StatusOK}
		start := time.Now()
		h(rec, r)
		ok := rec.code < 400
		audit.Log(audit.Entry{Event: "gui", Origin: "gui", Lab: s.lab.Root, Action: r.Method + " " + r.URL.Path,
			Args: params, OK: &ok, Duration: time.Since(start).Milliseconds()})
	}
}

// auditParams aplatit les paramètres d'une requête pour le journal, sans contenu de fichier ni secret.
func auditParams(prefix string, m map[string]any) []string {
	var out []string
	for k, v := range m {
		switch strings.ToLower(k) {
		case "text", "content", "data", "password":
			continue
		}
		if sub, ok := v.(map[string]any); ok {
			out = append(out, auditParams(prefix+k+".", sub)...)
			continue
		}
		out = append(out, fmt.Sprintf("%s%s=%v", prefix, k, v))
	}
	sort.Strings(out)
	return out
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
