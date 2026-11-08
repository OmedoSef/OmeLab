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
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"labs/labctl/internal/audit"
	"labs/labctl/internal/lab"
	"labs/labctl/internal/module"
	"labs/labctl/internal/virt"
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

// Ouverture de l'interface au lancement de labctl gui
type Open int

const (
	OpenBrowser Open = iota // navigateur par défaut (xdg-open)
	OpenApp                 // fenêtre d'application dédiée ; sa fermeture arrête le serveur
	OpenNone
)

// appBrowsers : navigateurs qui savent ouvrir une fenêtre sans barre d'adresse (--app)
var appBrowsers = []string{"chromium", "chromium-browser", "google-chrome", "google-chrome-stable", "microsoft-edge", "brave-browser"}

// startApp ouvre url dans une fenêtre d'application. Un profil à part (dans le cache de l'utilisateur) fait du
// navigateur un processus à lui : il se termine à la fermeture de la fenêtre, ce que signale done.
func startApp(url string) (done <-chan struct{}, err error) {
	bin := ""
	for _, b := range appBrowsers {
		if p, err := exec.LookPath(b); err == nil {
			bin = p
			break
		}
	}
	if bin == "" {
		return nil, fmt.Errorf("aucun navigateur compatible (%s)", strings.Join(appBrowsers, ", "))
	}
	cache, err := os.UserCacheDir()
	if err != nil {
		return nil, err
	}
	profile := filepath.Join(cache, "labctl", "gui-app")
	if err := os.MkdirAll(profile, 0o700); err != nil {
		return nil, err
	}
	cmd := exec.Command(bin, "--app="+url, "--user-data-dir="+profile, "--class=labctl", "--window-size=1320,880",
		"--no-first-run", "--no-default-browser-check")
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	ch := make(chan struct{})
	go func() { cmd.Wait(); close(ch) }()
	return ch, nil
}

// csp : politique de sécurité de la page de la GUI (voir Serve).
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

	srv := &http.Server{Handler: s.checkHost(mux), ReadHeaderTimeout: 10 * time.Second}
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

// handlePKITrust sonde en SSH quelles VMs font confiance à la racine / à l'autorité d'interception. Coûteux
// (un aller-retour par VM) : déclenché par le bouton « Vérifier la confiance », jamais par le rafraîchissement.
func (s *server) handlePKITrust(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.lab.TrustStatus(s.lab.AllVMNames()...))
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
	Name       string              `json:"name"`
	Subnet     string              `json:"subnet"`
	Domain     string              `json:"domain"`
	Hosts      []configHost        `json:"hosts"`
	Aliases    map[string]string   `json:"aliases"`
	Links      []string            `json:"links"`
	Conns      []module.Connection `json:"connections"` // connections: du module.yaml, dans l'ordre du fichier
	Vulnerable bool                `json:"vulnerable"`  // vulnerable: true : pas de connexion sortante
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
		cm := configModule{Name: m.Name, Subnet: m.Env.Subnet, Domain: m.Env.Domain, Aliases: m.Conf.Aliases, Links: m.Conf.Links,
			Conns: []module.Connection{}, Vulnerable: m.Vulnerable()}
		if m.YAML != nil && m.YAML.Connections != nil {
			cm.Conns = m.YAML.Connections
		}
		for _, vm := range m.Env.VMs() {
			cm.Hosts = append(cm.Hosts, configHost{vm, m.Env.IP(vm), m.Env.Macs[vm], "vm"})
		}
		for h, o := range m.Conf.Hosts {
			src := "reservation"
			if m.Conf.Attached[h] != "" {
				src = "attach:" + m.Conf.Attached[h] // VM d'un autre module branchée ici (attach: du module.yaml)
			}
			cm.Hosts = append(cm.Hosts, configHost{h, fmt.Sprintf("%s.%d", m.Env.Subnet, o), m.Conf.HostMACs[h], src})
		}
		out = append(out, cm)
	}
	resp := map[string]any{"modules": out, "gateway": s.lab.DomState(lab.GatewayVM)}
	if resp["gateway"] == virt.StateRunning {
		if changes, err := s.lab.PendingCoreChanges(); err != nil {
			resp["pendingError"] = err.Error()
		} else {
			resp["pending"] = changes
		}
	}
	writeJSON(w, resp)
}

type configEdit struct {
	Op     string         `json:"op"` // host-add, host-del, alias-add, alias-del, link, unlink, conn-add, conn-del
	Module string         `json:"module"`
	Name   string         `json:"name"`
	Octet  int            `json:"octet"`
	MAC    string         `json:"mac"`
	Target string         `json:"target"`
	From   []string       `json:"from"`   // conn-add
	Ports  []string       `json:"ports"`  // conn-add
	Via    string         `json:"via"`    // conn-add : direct | portal
	Portal *module.Portal `json:"portal"` // conn-add, via portal
	Proxy  *module.Proxy  `json:"proxy"`  // conn-add, via proxy
	Local  bool           `json:"local"`  // conn-add : écrire dans network.local.yaml (propre au poste), pas le module.yaml
	Index  int            `json:"index"`  // conn-del
}

// handleConfigEdit modifie la section dns du module.yaml (sans appliquer : voir « Appliquer » dans l'onglet Réseau).
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
	case "conn-add":
		cn := module.Connection{To: e.Target, From: e.From, Ports: e.Ports, Via: e.Via, Portal: e.Portal, Proxy: e.Proxy}
		if e.Local {
			err = s.lab.AddLocalConnection(e.Module, cn)
		} else {
			err = s.lab.ConfAddConnection(e.Module, cn)
		}
	case "conn-del":
		err = s.lab.DelConnectionAt(e.Module, e.Index)
	case "alias-add":
		err = s.lab.ConfAddAlias(e.Module, e.Name, e.Target)
	case "alias-del":
		err = s.lab.ConfDelAlias(e.Module, e.Name)
	case "link", "unlink":
		err = s.lab.ConfLink(e.Module, e.Target, e.Op == "link")
	case "attach-add":
		err = s.lab.AttachVM(e.Module, e.Target, e.Octet, io.Discard)
	case "attach-del":
		err = s.lab.DetachVM(e.Module, e.Target, io.Discard)
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

// --- Fichiers complémentaires d'un module (modules/<m>/files/)

func (s *server) handleFiles(w http.ResponseWriter, r *http.Request) {
	fl, err := s.lab.ModuleFiles(r.URL.Query().Get("module"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, fl)
}

func (s *server) handleFileText(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	text, hash, err := s.lab.ModuleFileText(q.Get("module"), q.Get("path"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, map[string]string{"text": text, "hash": hash})
}

// handleFileRaw renvoie le fichier en pièce jointe (jamais affiché par le navigateur).
func (s *server) handleFileRaw(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	p, err := s.lab.ModuleFilePath(q.Get("module"), q.Get("path"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filepath.Base(p)))
	http.ServeFile(w, r, p)
}

type fileSave struct {
	Module string `json:"module"`
	Path   string `json:"path"`
	Text   string `json:"text"`
	Hash   string `json:"hash"`
	Create bool   `json:"create"`
}

func (s *server) handleFileSave(w http.ResponseWriter, r *http.Request) {
	var req fileSave
	if err := json.NewDecoder(io.LimitReader(r.Body, lab.MaxTextFile*2)).Decode(&req); err != nil {
		http.Error(w, "requête invalide", http.StatusBadRequest)
		return
	}
	hash, err := s.lab.SaveModuleFileText(req.Module, req.Path, req.Text, req.Hash, req.Create)
	if err != nil {
		code := http.StatusBadRequest
		if errors.Is(err, lab.ErrConflict) || errors.Is(err, lab.ErrExists) {
			code = http.StatusConflict
		}
		http.Error(w, err.Error(), code)
		return
	}
	writeJSON(w, map[string]string{"hash": hash})
}

// handleFileUpload : corps brut = contenu du fichier ; module, path et overwrite dans l'adresse.
func (s *server) handleFileUpload(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	body := http.MaxBytesReader(w, r.Body, lab.MaxUpload+1)
	err := s.lab.UploadModuleFile(q.Get("module"), q.Get("path"), body, q.Get("overwrite") == "1")
	if err != nil {
		code := http.StatusBadRequest
		if errors.Is(err, lab.ErrExists) {
			code = http.StatusConflict
		}
		http.Error(w, err.Error(), code)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

type fileOp struct {
	Module string `json:"module"`
	Op     string `json:"op"` // mkdir, rename, delete
	Path   string `json:"path"`
	To     string `json:"to"`
}

func (s *server) handleFileOp(w http.ResponseWriter, r *http.Request) {
	var req fileOp
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<12)).Decode(&req); err != nil {
		http.Error(w, "requête invalide", http.StatusBadRequest)
		return
	}
	var err error
	switch req.Op {
	case "mkdir":
		err = s.lab.MkdirModuleFile(req.Module, req.Path)
	case "rename":
		err = s.lab.RenameModuleFile(req.Module, req.Path, req.To)
	case "delete":
		err = s.lab.DeleteModuleFile(req.Module, req.Path)
	default:
		err = fmt.Errorf("opération inconnue : %q", req.Op)
	}
	if err != nil {
		code := http.StatusBadRequest
		if errors.Is(err, lab.ErrExists) {
			code = http.StatusConflict
		}
		http.Error(w, err.Error(), code)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

// --- Proxy de la passerelle : journal, interface mitmweb (tunnel SSH)

func (s *server) handleProxyLog(w http.ResponseWriter, r *http.Request) {
	n := 300
	fmt.Sscan(r.URL.Query().Get("n"), &n)
	if n < 1 || n > 5000 {
		n = 300
	}
	ev, err := s.lab.Gateway(nil).ProxyLog(n)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	if ev == nil {
		ev = []lab.ProxyEvent{}
	}
	writeJSON(w, ev)
}

// handleProxyWeb ouvre (ou réutilise) le tunnel vers mitmweb et renvoie son adresse et son mot de passe.
func (s *server) handleProxyWeb(w http.ResponseWriter, r *http.Request) {
	s.tunnel.Lock()
	defer s.tunnel.Unlock()
	g := s.lab.Gateway(nil)
	alive := s.tunDone != nil
	if alive {
		select {
		case <-s.tunDone:
			alive = false
		default:
		}
	}
	if !alive {
		addr, cmd, err := g.ProxyTunnel()
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		done := make(chan struct{})
		s.tunAddr, s.tunCmd, s.tunDone = addr, cmd, done
		go func() { _ = cmd.Wait(); close(done) }()
	}
	pw := g.ProxyWebPassword()
	writeJSON(w, map[string]string{"url": "http://" + s.tunAddr + "/?token=" + url.QueryEscape(pw), "password": pw})
}
