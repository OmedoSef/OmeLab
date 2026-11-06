package portal

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"labs/labctl/internal/coreconf"
)

func testServer(t *testing.T) (*Server, *[]string, *time.Time) {
	t.Helper()
	confs := []coreconf.PortalConf{
		{ID: 0, Port: 8800, Set: "portal_0", Module: "demo", Gateway: "10.77.77.254", Sources: []string{"10.77.77.10"},
			Target: "internet", Auth: "password", Password: "lab", Title: "Portail", Seconds: 60, Detect: true},
		{ID: 1, Port: 8801, Set: "portal_1", Module: "demo", Gateway: "10.77.77.254", Sources: []string{"10.77.77.0/24"},
			Target: "pentest/web", Auth: "click", Title: "Portail web", Seconds: 3600},
	}
	state := filepath.Join(t.TempDir(), "sessions.json")
	s := New(confs, state)
	var cmds []string
	now := time.Date(2026, 11, 6, 10, 0, 0, 0, time.UTC)
	s.nft = func(c string) error { cmds = append(cmds, c); return nil }
	s.flushCT = func(string) {}
	s.now = func() time.Time { return now }
	return s, &cmds, &now
}

func do(h http.Handler, method, target, host, from string, form url.Values) *httptest.ResponseRecorder {
	var body *strings.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	} else {
		body = strings.NewReader("")
	}
	r := httptest.NewRequest(method, target, body)
	r.Host, r.RemoteAddr = host, from+":40000"
	if form != nil {
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestPortalFlow(t *testing.T) {
	s, cmds, now := testServer(t)
	h := s.Handler(0)
	self := "10.77.77.254:8800"

	// Requête captée : redirection vers le portail, URL d'origine gardée
	w := do(h, "GET", "/generate_204", "connectivitycheck.gstatic.com", "10.77.77.10", nil)
	if w.Code != http.StatusFound || !strings.HasPrefix(w.Header().Get("Location"), "http://"+self+"/?url=http://connectivitycheck.gstatic.com/generate_204") {
		t.Fatalf("redirection : %d %s", w.Code, w.Header().Get("Location"))
	}
	// Source hors du portail : refusée
	if w := do(h, "GET", "/", self, "10.77.77.11", nil); w.Code != http.StatusForbidden {
		t.Errorf("source étrangère : %d", w.Code)
	}
	// API RFC 8908 : captif
	w = do(h, "GET", "/api", self, "10.77.77.10", nil)
	var api map[string]any
	json.Unmarshal(w.Body.Bytes(), &api)
	if w.Header().Get("Content-Type") != "application/captive+json" || api["captive"] != true {
		t.Errorf("api : %s %v", w.Header().Get("Content-Type"), api)
	}
	// Mauvais mot de passe : page avec erreur, rien dans nftables
	w = do(h, "POST", "/login", self, "10.77.77.10", url.Values{"password": {"non"}})
	if !strings.Contains(w.Body.String(), "Mot de passe incorrect") || len(*cmds) != 0 {
		t.Errorf("mauvais mot de passe : %v", *cmds)
	}
	// Bon mot de passe : source ajoutée avec expiration, retour vers l'URL d'origine
	w = do(h, "POST", "/login", self, "10.77.77.10", url.Values{"password": {"lab"}, "url": {"http://example.org/x"}})
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "http://example.org/x" {
		t.Errorf("connexion : %d %s", w.Code, w.Header().Get("Location"))
	}
	if last := (*cmds)[len(*cmds)-1]; last != "add element inet gw portal_0 { 10.77.77.10 timeout 60s }" {
		t.Errorf("nft : %q", last)
	}
	if ss := s.Sessions(); len(ss) != 1 || !ss[0].Expires.Equal(now.Add(time.Minute)) {
		t.Errorf("sessions : %+v", ss)
	}
	w = do(h, "GET", "/api", self, "10.77.77.10", nil)
	json.Unmarshal(w.Body.Bytes(), &api)
	if api["captive"] != false || api["seconds-remaining"] != float64(60) {
		t.Errorf("api connecté : %v", api)
	}

	// Reprise après redémarrage du service : la session est relue et remise dans nftables avec le temps restant
	*now = now.Add(20 * time.Second)
	s2 := New([]coreconf.PortalConf{s.confs[0], s.confs[1]}, s.state)
	var cmds2 []string
	s2.nft = func(c string) error { cmds2 = append(cmds2, c); return nil }
	s2.flushCT = func(string) {}
	s2.now = s.now
	s2.Resync()
	if len(cmds2) != 1 || cmds2[0] != "add element inet gw portal_0 { 10.77.77.10 timeout 40s }" {
		t.Errorf("reprise : %v", cmds2)
	}
	// Expiration : la session disparaît
	*now = now.Add(time.Minute)
	s2.Resync()
	if len(s2.Sessions()) != 0 {
		t.Error("session expirée gardée")
	}
}

func TestPortalVariants(t *testing.T) {
	s, cmds, _ := testServer(t)
	h := s.Handler(1)
	self := "10.77.77.254:8801"
	// detect: false : les tests de connectivité reçoivent la réponse « connecté »
	if w := do(h, "GET", "/generate_204", "connectivitycheck.gstatic.com", "10.77.77.50", nil); w.Code != http.StatusNoContent {
		t.Errorf("generate_204 : %d", w.Code)
	}
	if w := do(h, "GET", "/hotspot-detect.html", "captive.apple.com", "10.77.77.50", nil); !strings.Contains(w.Body.String(), "Success") {
		t.Errorf("apple : %s", w.Body.String())
	}
	// ... mais la navigation reste captée
	if w := do(h, "GET", "/", "10.66.66.30", "10.77.77.50", nil); w.Code != http.StatusFound {
		t.Errorf("navigation : %d", w.Code)
	}
	// Clic : connexion directe ; URL externe à la passerelle seulement
	w := do(h, "POST", "/login", self, "10.77.77.50", url.Values{"url": {"javascript:alert(1)"}})
	if w.Header().Get("Location") != "/" || len(*cmds) != 2 {
		t.Errorf("clic : %s %v", w.Header().Get("Location"), *cmds)
	}
	// Déconnexion par la commande locale
	c := s.Control()
	if w := do(c, "POST", "/kick?portal=1&ip=10.77.77.50", "127.0.0.1:8799", "127.0.0.1", nil); w.Code != 200 {
		t.Errorf("kick : %d", w.Code)
	}
	if last := (*cmds)[len(*cmds)-1]; last != "delete element inet gw portal_1 { 10.77.77.50 }" || len(s.Sessions()) != 0 {
		t.Errorf("après kick : %v", *cmds)
	}
}
