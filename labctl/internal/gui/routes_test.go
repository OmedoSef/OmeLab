package gui

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// routesServer : serveur de test complet (routeur réel), avec un module « fic », un fichier dans files/, un secret
// hors du module et une PKI (certificat public et clé privée).
func routesServer(t *testing.T) (*server, http.Handler) {
	t.Helper()
	s := testServer(t)
	s.host = "127.0.0.1:8077"
	root := s.lab.Root
	t.Setenv("LABS_PKI_DIR", filepath.Join(root, "pki"))
	t.Setenv("LABCTL_AUDIT", "off")
	for p, content := range map[string]string{
		"modules/fic/module.yaml":    "module: fic\n",
		"modules/fic/files/app.conf": "a=1\n",
		"secret.txt":                 "SECRET\n",
		"pki/root/root.crt":          "-----BEGIN CERTIFICATE-----\nMII\n-----END CERTIFICATE-----\n",
		"pki/root/root.key":          "-----BEGIN PRIVATE KEY-----\nMII\n-----END PRIVATE KEY-----\n",
		"pki/root/piege.crt":         "-----BEGIN PRIVATE KEY-----\nMII\n-----END PRIVATE KEY-----\n",
	} {
		f := filepath.Join(root, p)
		if err := os.MkdirAll(filepath.Dir(f), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(f, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return s, s.routes()
}

func call(h http.Handler, method, target, host, token, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.Host = host
	if token != "" {
		req.Header.Set("X-Lab-Token", token)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// Le routeur complet : contrôle de l'hôte (DNS rebinding) avant tout, jeton sur toute l'API, CSP sur la page.
func TestRoutesHostTokenAndCSP(t *testing.T) {
	s, h := routesServer(t)
	for _, c := range []struct {
		name, method, target, host, token string
		want                              int
	}{
		{"hôte étranger, même avec le jeton", "GET", "/api/files?module=fic", "evil.example:8077", s.token, http.StatusForbidden},
		{"hôte étranger sur la page", "GET", "/", "evil.example:8077", "", http.StatusForbidden},
		{"autre port", "GET", "/api/files?module=fic", "127.0.0.1:9999", s.token, http.StatusForbidden},
		{"sans jeton", "GET", "/api/files?module=fic", s.host, "", http.StatusUnauthorized},
		{"mauvais jeton", "POST", "/api/file/op", s.host, "x", http.StatusUnauthorized},
		{"localhost accepté", "GET", "/api/files?module=fic", "localhost:8077", s.token, http.StatusOK},
		{"page", "GET", "/", s.host, "", http.StatusOK},
	} {
		if rec := call(h, c.method, c.target, c.host, c.token, ""); rec.Code != c.want {
			t.Errorf("%s : %d, attendu %d (%s)", c.name, rec.Code, c.want, strings.TrimSpace(rec.Body.String()))
		}
	}
	rec := call(h, "GET", "/", s.host, "", "")
	if csp := rec.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "script-src 'self'") || strings.Contains(csp, "unsafe-eval") {
		t.Errorf("CSP de la page : %q", csp)
	}
}

// Aucune route ne sort du dossier du module ni ne livre une clé de la PKI.
func TestRoutesConfinement(t *testing.T) {
	s, h := routesServer(t)
	for _, target := range []string{
		"/api/file?module=fic&path=../../../secret.txt",
		"/api/file?module=fic&path=/etc/passwd",
		"/api/file/raw?module=fic&path=../module.yaml",
		"/api/file?module=../..&path=secret.txt",
		"/api/files?module=../../",
		"/api/module?name=../../secret",
		"/api/pki/file?path=root/root.key",
		"/api/pki/file?path=root/piege.crt",
		"/api/pki/file?path=../secret.txt",
	} {
		rec := call(h, "GET", target, s.host, s.token, "")
		if rec.Code == http.StatusOK || strings.Contains(rec.Body.String(), "SECRET") || strings.Contains(rec.Body.String(), "PRIVATE KEY") {
			t.Errorf("%s : %d %q", target, rec.Code, rec.Body.String())
		}
	}
	// Les accès légitimes passent
	if rec := call(h, "GET", "/api/file?module=fic&path=app.conf", s.host, s.token, ""); rec.Code != http.StatusOK {
		t.Errorf("lecture de files/app.conf : %d %s", rec.Code, rec.Body.String())
	}
	if rec := call(h, "GET", "/api/pki/file?path=root/root.crt", s.host, s.token, ""); rec.Code != http.StatusOK {
		t.Errorf("certificat public : %d %s", rec.Code, rec.Body.String())
	}
}

// Enregistrer un fichier modifié ailleurs depuis sa lecture est refusé (conflit), et une seule action à la fois.
func TestRoutesConflictAndBusy(t *testing.T) {
	s, h := routesServer(t)
	rec := call(h, "POST", "/api/file/save", s.host, s.token, `{"module":"fic","path":"app.conf","text":"a=2\n","hash":"pas-la-bonne"}`)
	if rec.Code != http.StatusConflict {
		t.Errorf("enregistrement sur une version périmée : %d %s", rec.Code, rec.Body.String())
	}
	if b, _ := os.ReadFile(filepath.Join(s.lab.Root, "modules/fic/files/app.conf")); string(b) != "a=1\n" {
		t.Errorf("fichier modifié malgré le conflit : %q", b)
	}
	s.busy.Lock() // une action en cours
	defer s.busy.Unlock()
	if rec := call(h, "POST", "/api/run", s.host, s.token, `{"args":["status"]}`); rec.Code != http.StatusConflict {
		t.Errorf("seconde action pendant la première : %d", rec.Code)
	}
}
