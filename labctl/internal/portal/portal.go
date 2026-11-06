// Package portal est le service de portail captif de la passerelle (labctl gw-portal serve, sur core-gw).
//
// Chaque connexion via: portal a son port (gwdns/portal.json, généré par coreconf). Le pare-feu redirige vers
// ce port le HTTP des sources non authentifiées ; le portail les authentifie (clic, mot de passe ou conditions)
// en ajoutant leur adresse à l'ensemble nftables du portail, avec une expiration : nftables la retire seul à la
// fin de la session. Les sessions sont aussi gardées dans un fichier (/run/gw) pour être remises en place après
// un « gwctl apply » (qui recrée les ensembles vides) ou un redémarrage du service.
package portal

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"labs/labctl/internal/coreconf"
)

// Chemins sur la passerelle.
const (
	ConfigPath = "/etc/gwdns/portal.json"
	StatePath  = "/run/gw/portal-sessions.json"
)

// Session : une source authentifiée sur un portail.
type Session struct {
	Portal  int       `json:"portal"`
	IP      string    `json:"ip"`
	Module  string    `json:"module"`
	Target  string    `json:"target"`
	Since   time.Time `json:"since"`
	Expires time.Time `json:"expires"`
}

// Server sert les portails et tient leurs sessions.
type Server struct {
	confs   map[int]coreconf.PortalConf
	state   string
	nft     func(cmd string) error // commande nftables (remplacée dans les tests)
	flushCT func(ip string)        // ferme les connexions suivies d'une source (remplacée dans les tests)
	now     func() time.Time

	mu       sync.Mutex
	sessions map[string]*Session // clé : <portail>/<ip>
}

// LoadConfig lit la configuration des portails.
func LoadConfig(path string) ([]coreconf.PortalConf, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var c []coreconf.PortalConf
	return c, json.Unmarshal(b, &c)
}

// New prépare le service ; les sessions encore valides du fichier d'état sont reprises.
func New(confs []coreconf.PortalConf, state string) *Server {
	s := &Server{confs: map[int]coreconf.PortalConf{}, state: state, now: time.Now, nft: runNft, flushCT: flushConntrack, sessions: map[string]*Session{}}
	for _, c := range confs {
		s.confs[c.ID] = c
	}
	var old []*Session
	if b, err := os.ReadFile(state); err == nil && json.Unmarshal(b, &old) == nil {
		for _, se := range old {
			// même portail (numéro, module, cible) ; les sessions expirées partent au premier Resync
			if c, ok := s.confs[se.Portal]; ok && c.Module == se.Module && c.Target == se.Target {
				s.sessions[key(se.Portal, se.IP)] = se
			}
		}
	}
	return s
}

func runNft(cmd string) error {
	out, err := exec.Command("nft", strings.Fields(cmd)...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("nft %s : %v %s", cmd, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// flushConntrack ferme les connexions déjà suivies dont ip est la source : sans ça, retirer ip de
// l'ensemble du portail ne coupe pas les flux en cours (la chaîne forward accepte « established,related »),
// et une déconnexion (ou une expiration) ne couperait l'accès qu'aux nouvelles connexions.
func flushConntrack(ip string) {
	bin := "conntrack"
	if _, err := exec.LookPath(bin); err != nil {
		bin = "/usr/sbin/conntrack" // emplacement Debian si /usr/sbin n'est pas dans le PATH
	}
	_ = exec.Command(bin, "-D", "-s", ip).Run() // best-effort : échoue sans entrée, ou si l'outil manque
}

func key(id int, ip string) string { return fmt.Sprintf("%d/%s", id, ip) }

// Resync retire les sessions expirées et remet les autres dans les ensembles nftables (après un
// « gwctl apply », ils sont vides), avec leur durée restante.
func (s *Server) Resync() {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	for k, se := range s.sessions {
		left := int(se.Expires.Sub(now).Seconds())
		if left <= 0 {
			delete(s.sessions, k)
			s.flushCT(se.IP) // session expirée : on coupe aussi les flux encore ouverts
			continue
		}
		_ = s.nft(fmt.Sprintf("add element inet gw %s { %s timeout %ds }", s.confs[se.Portal].Set, se.IP, left))
	}
	s.save()
}

func (s *Server) save() {
	list := s.list()
	b, _ := json.MarshalIndent(list, "", "  ")
	if err := os.MkdirAll(filepath.Dir(s.state), 0o755); err == nil {
		tmp := s.state + ".tmp"
		if os.WriteFile(tmp, b, 0o600) == nil {
			_ = os.Rename(tmp, s.state)
		}
	}
}

// list renvoie les sessions valides, triées (verrou tenu par l'appelant).
func (s *Server) list() []*Session {
	out := []*Session{}
	now := s.now()
	for _, se := range s.sessions {
		if se.Expires.After(now) {
			out = append(out, se)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Portal != out[j].Portal {
			return out[i].Portal < out[j].Portal
		}
		return out[i].IP < out[j].IP
	})
	return out
}

// Sessions renvoie les sessions en cours.
func (s *Server) Sessions() []*Session {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.list()
}

// Login ouvre (ou renouvelle) la session de ip sur le portail id.
func (s *Server) Login(id int, ip string) (*Session, error) {
	c, ok := s.confs[id]
	if !ok {
		return nil, fmt.Errorf("portail %d inconnu", id)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	se := &Session{Portal: id, IP: ip, Module: c.Module, Target: c.Target, Since: now, Expires: now.Add(time.Duration(c.Seconds) * time.Second)}
	_ = s.nft(fmt.Sprintf("delete element inet gw %s { %s }", c.Set, ip)) // renouvellement : nouvelle expiration
	if err := s.nft(fmt.Sprintf("add element inet gw %s { %s timeout %ds }", c.Set, ip, c.Seconds)); err != nil {
		return nil, err
	}
	s.sessions[key(id, ip)] = se
	s.save()
	return se, nil
}

// Logout ferme la session de ip sur le portail id (false si elle n'existait pas).
func (s *Server) Logout(id int, ip string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	se, ok := s.sessions[key(id, ip)]
	if !ok {
		return false
	}
	delete(s.sessions, key(id, ip))
	_ = s.nft(fmt.Sprintf("delete element inet gw %s { %s }", s.confs[id].Set, se.IP))
	s.flushCT(se.IP) // coupe aussi les connexions en cours, pas seulement les nouvelles
	s.save()
	return true
}

func (s *Server) session(id int, ip string) *Session {
	s.mu.Lock()
	defer s.mu.Unlock()
	if se, ok := s.sessions[key(id, ip)]; ok && se.Expires.After(s.now()) {
		return se
	}
	return nil
}

// covers : l'adresse fait-elle partie des sources du portail ?
func covers(c coreconf.PortalConf, ip string) bool {
	a, err := netip.ParseAddr(ip)
	if err != nil {
		return false
	}
	for _, src := range c.Sources {
		if p, err := netip.ParsePrefix(src); err == nil && p.Contains(a) {
			return true
		}
		if b, err := netip.ParseAddr(src); err == nil && b == a {
			return true
		}
	}
	return false
}

func clientIP(r *http.Request) string {
	h, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return h
}

// Serve écoute sur le port de chaque portail et sur la commande locale, jusqu'à l'annulation de ctx.
// Les sessions sont remises en place au démarrage, puis toutes les 30 s.
func (s *Server) Serve(ctx context.Context) error {
	s.Resync()
	var srvs []*http.Server
	errs := make(chan error, len(s.confs)+1)
	start := func(addr string, h http.Handler) {
		srv := &http.Server{Addr: addr, Handler: h, ReadHeaderTimeout: 10 * time.Second}
		srvs = append(srvs, srv)
		go func() {
			if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				errs <- fmt.Errorf("%s : %w", addr, err)
			}
		}()
	}
	for _, c := range s.confs {
		start(fmt.Sprintf(":%d", c.Port), s.Handler(c.ID))
	}
	start(fmt.Sprintf("127.0.0.1:%d", coreconf.PortalControlPort), s.Control())
	tick := time.NewTicker(30 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			for _, srv := range srvs {
				_ = srv.Close()
			}
			return nil
		case err := <-errs:
			return err
		case <-tick.C:
			s.Resync()
		}
	}
}

// Control : commande locale (127.0.0.1) : GET /sessions, POST /kick?portal=N&ip=A.
func (s *Server) Control() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /sessions", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(s.Sessions())
	})
	mux.HandleFunc("POST /kick", func(w http.ResponseWriter, r *http.Request) {
		var id int
		if _, err := fmt.Sscan(r.URL.Query().Get("portal"), &id); err != nil {
			http.Error(w, "portail invalide", http.StatusBadRequest)
			return
		}
		if !s.Logout(id, r.URL.Query().Get("ip")) {
			http.Error(w, "pas de session", http.StatusNotFound)
			return
		}
		fmt.Fprintln(w, "ok")
	})
	return mux
}

// Handler sert un portail : page, connexion, déconnexion, API RFC 8908, et redirection des requêtes captées.
func (s *Server) Handler(id int) http.Handler {
	c := s.confs[id]
	self := fmt.Sprintf("%s:%d", c.Gateway, c.Port)
	base := "http://" + self
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		ip := clientIP(r)
		if !covers(c, ip) {
			http.Error(w, fmt.Sprintf("Ce portail ne concerne pas %s.", ip), http.StatusForbidden)
			return
		}
		// Requête captée par le pare-feu (adressée à un autre site) : test de connectivité ou navigation
		if r.Host != self && r.Host != c.Gateway {
			if !c.Detect {
				if body, code, ok := probeAnswer(r.Host, r.URL.Path); ok {
					w.WriteHeader(code)
					fmt.Fprint(w, body)
					return
				}
			}
			orig := "http://" + r.Host + r.URL.RequestURI()
			loc := base + "/?url=" + urlQuery(orig)
			w.Header().Set("Location", loc)
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusFound)
			fmt.Fprintf(w, `<html><body>Authentification demandée : <a href="%s">%s</a></body></html>`, htmlEsc(loc), htmlEsc(c.Title))
			return
		}
		switch {
		case r.URL.Path == "/api" && r.Method == http.MethodGet:
			w.Header().Set("Content-Type", "application/captive+json")
			se := s.session(id, ip)
			resp := map[string]any{"captive": se == nil, "user-portal-url": base + "/"}
			if se != nil {
				resp["seconds-remaining"] = int(se.Expires.Sub(s.now()).Seconds())
			}
			_ = json.NewEncoder(w).Encode(resp)
		case r.URL.Path == "/login" && r.Method == http.MethodPost:
			_ = r.ParseForm()
			if msg := checkAuth(c, r); msg != "" {
				s.page(w, c, ip, r.FormValue("url"), msg)
				return
			}
			if _, err := s.Login(id, ip); err != nil {
				s.page(w, c, ip, r.FormValue("url"), "Erreur de la passerelle : "+err.Error())
				return
			}
			if u := r.FormValue("url"); strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, base) {
				http.Redirect(w, r, u, http.StatusSeeOther)
				return
			}
			http.Redirect(w, r, "/", http.StatusSeeOther)
		case r.URL.Path == "/logout" && r.Method == http.MethodPost:
			s.Logout(id, ip)
			http.Redirect(w, r, "/", http.StatusSeeOther)
		case r.URL.Path == "/":
			s.page(w, c, ip, r.URL.Query().Get("url"), "")
		default:
			http.NotFound(w, r)
		}
	})
}

// checkAuth vérifie le formulaire selon le mode du portail ("" : accepté).
func checkAuth(c coreconf.PortalConf, r *http.Request) string {
	switch c.Auth {
	case "password":
		if subtle.ConstantTimeCompare([]byte(r.FormValue("password")), []byte(c.Password)) != 1 {
			return "Mot de passe incorrect."
		}
	case "terms":
		if r.FormValue("terms") == "" {
			return "Il faut accepter les conditions d'utilisation."
		}
	}
	return ""
}

// probeAnswer : réponse « connecté » aux tests de connectivité connus (detect: false, portail discret).
func probeAnswer(host, path string) (body string, code int, ok bool) {
	host = strings.ToLower(strings.Split(host, ":")[0])
	switch {
	case strings.HasSuffix(path, "/generate_204") || path == "/gen_204":
		return "", http.StatusNoContent, true
	case host == "captive.apple.com" || strings.HasSuffix(path, "/hotspot-detect.html"):
		return "<HTML><HEAD><TITLE>Success</TITLE></HEAD><BODY>Success</BODY></HTML>", http.StatusOK, true
	case host == "www.msftconnecttest.com" && path == "/connecttest.txt":
		return "Microsoft Connect Test", http.StatusOK, true
	case host == "www.msftncsi.com" && path == "/ncsi.txt":
		return "Microsoft NCSI", http.StatusOK, true
	case host == "detectportal.firefox.com":
		return "success\n", http.StatusOK, true
	case host == "nmcheck.gnome.org" || host == "network-test.debian.org" || host == "connectivity-check.ubuntu.com":
		return "NetworkManager is online\n", http.StatusOK, true
	}
	return "", 0, false
}

func urlQuery(s string) string {
	r := strings.NewReplacer("%", "%25", "&", "%26", "#", "%23", "+", "%2B", " ", "%20", "?", "%3F", "=", "%3D")
	return r.Replace(s)
}

func htmlEsc(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&#39;").Replace(s)
}

func (s *Server) page(w http.ResponseWriter, c coreconf.PortalConf, ip, url, msg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	target := "à Internet"
	if c.Target != "internet" {
		target = "au module " + c.Target
	}
	var body string
	if se := s.session(c.ID, ip); se != nil {
		left := se.Expires.Sub(s.now()).Round(time.Second)
		body = fmt.Sprintf(`<p class="ok">Connecté : accès %s jusqu'à %s (reste %s).</p>
<form method="post" action="/logout"><button>Se déconnecter</button></form>`, htmlEsc(target), se.Expires.Local().Format("15:04:05"), left)
	} else {
		field := ""
		switch c.Auth {
		case "password":
			field = `<label>Mot de passe<input type="password" name="password" autofocus required></label>`
		case "terms":
			field = `<label class="terms"><input type="checkbox" name="terms" required> J'accepte les conditions d'utilisation du réseau.</label>`
		}
		if msg != "" {
			msg = `<p class="err">` + htmlEsc(msg) + `</p>`
		}
		body = fmt.Sprintf(`<p>Pour accéder %s depuis %s, authentifiez-vous.</p>%s
<form method="post" action="/login"><input type="hidden" name="url" value="%s">%s<button>Se connecter</button></form>`,
			htmlEsc(target), htmlEsc(ip), msg, htmlEsc(url), field)
	}
	fmt.Fprintf(w, `<!doctype html><html lang="fr"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>%s</title><style>
body{font:15px/1.5 system-ui,sans-serif;background:#f4f5f7;color:#1d2330;margin:0;display:grid;place-items:center;min-height:100vh}
main{background:#fff;border:1px solid #e3e6eb;border-radius:12px;padding:28px 32px;max-width:420px;width:calc(100%% - 32px)}
h1{font-size:20px;margin:0 0 12px} form{display:grid;gap:12px;margin-top:16px} label{display:grid;gap:4px;font-size:13px;color:#667085}
label.terms{display:flex;gap:8px;align-items:center;color:#1d2330} input[type=password]{padding:8px;border:1px solid #d0d5dd;border-radius:7px;font:inherit}
button{padding:9px 14px;border:0;border-radius:7px;background:#2f6fde;color:#fff;font:inherit;cursor:pointer}
.ok{color:#1e9e5a}.err{color:#d1433b}small{display:block;margin-top:18px;color:#98a2b3}
</style></head><body><main><h1>%s</h1>%s<small>Omedo Labs · portail captif de test · module %s</small></main></body></html>`,
		htmlEsc(c.Title), htmlEsc(c.Title), body, htmlEsc(c.Module))
}
