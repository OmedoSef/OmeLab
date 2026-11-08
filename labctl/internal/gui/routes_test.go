package gui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"labs/labctl/internal/lab"
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

// Opérations sur files/ par le vrai routeur : créer, déplacer, supprimer ; conflits et chemins hors du module refusés.
func TestRoutesFileOps(t *testing.T) {
	s, h := routesServer(t)
	files := filepath.Join(s.lab.Root, "modules", "fic", "files")
	for _, c := range []struct {
		body string
		want int
	}{
		{`{"module":"fic","op":"mkdir","path":"sous"}`, http.StatusOK},
		{`{"module":"fic","op":"rename","path":"app.conf","to":"sous/app.conf"}`, http.StatusOK},
		{`{"module":"fic","op":"mkdir","path":"sous"}`, http.StatusConflict},
		{`{"module":"fic","op":"mkdir","path":"../../../evasion"}`, http.StatusBadRequest},
		{`{"module":"fic","op":"rename","path":"sous/app.conf","to":"../module.yaml"}`, http.StatusBadRequest},
		{`{"module":"../..","op":"delete","path":"secret.txt"}`, http.StatusBadRequest},
		{`{"module":"fic","op":"chmod","path":"sous"}`, http.StatusBadRequest},
	} {
		if rec := call(h, "POST", "/api/file/op", s.host, s.token, c.body); rec.Code != c.want {
			t.Errorf("%s : %d, attendu %d (%s)", c.body, rec.Code, c.want, strings.TrimSpace(rec.Body.String()))
		}
	}
	if _, err := os.Stat(filepath.Join(files, "sous", "app.conf")); err != nil {
		t.Errorf("fichier non déplacé : %v", err)
	}
	if _, err := os.Stat(filepath.Join(s.lab.Root, "evasion")); err == nil {
		t.Error("dossier créé hors du module")
	}
	if rec := call(h, "POST", "/api/file/op", s.host, s.token, `{"module":"fic","op":"delete","path":"sous"}`); rec.Code != http.StatusOK {
		t.Errorf("suppression : %d", rec.Code)
	}
	if _, err := os.Stat(filepath.Join(s.lab.Root, "secret.txt")); err != nil {
		t.Error("fichier hors du module supprimé")
	}
	if rec := call(h, "GET", "/api/file/op?module=fic&op=delete&path=sous", s.host, s.token, ""); rec.Code < 400 {
		t.Errorf("GET sur une route d'écriture traité : %d", rec.Code) // seul POST y mène
	}
}

// Éditeur de module.yaml : validation (nom piégé refusé), création, conflit de version, texte invalide jamais écrit.
func TestRoutesModuleSave(t *testing.T) {
	s, h := routesServer(t)
	rec := call(h, "POST", "/api/module/check", s.host, s.token, `{"name":"../../x","text":"module: x\n"}`)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "nom de module invalide") {
		t.Errorf("validation d'un nom piégé : %d %s", rec.Code, rec.Body.String())
	}
	body, _ := json.Marshal(map[string]any{"name": "neuf", "text": lab.ModuleSkeleton("neuf"), "create": true})
	if rec := call(h, "POST", "/api/module/save", s.host, s.token, string(body)); rec.Code != http.StatusOK {
		t.Fatalf("création : %d %s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(s.lab.Root, "modules", "neuf", "module.yaml")); err != nil {
		t.Fatalf("module non créé : %v", err)
	}
	path := filepath.Join(s.lab.Root, "modules", "fic", "module.yaml")
	if rec := call(h, "POST", "/api/module/save", s.host, s.token, `{"name":"fic","text":"module: fic\n# x\n","hash":"périmée"}`); rec.Code != http.StatusConflict {
		t.Errorf("version périmée : %d %s", rec.Code, rec.Body.String())
	}
	_, hash, _ := s.lab.ModuleText("fic")
	body, _ = json.Marshal(map[string]any{"name": "fic", "text": "module: fic\ncle_inconnue: 1\n", "hash": hash})
	if rec := call(h, "POST", "/api/module/save", s.host, s.token, string(body)); rec.Code != http.StatusBadRequest {
		t.Errorf("texte invalide : %d %s", rec.Code, rec.Body.String())
	}
	if b, _ := os.ReadFile(path); string(b) != "module: fic\n" {
		t.Errorf("module.yaml modifié malgré le refus : %q", b)
	}
}

// Page Réseau : un alias s'ajoute au module.yaml ; nom invalide, opération inconnue et action concurrente refusés.
func TestRoutesConfigEdit(t *testing.T) {
	s, h := routesServer(t)
	dir := filepath.Join(s.lab.Root, "modules", "web")
	_ = os.MkdirAll(dir, 0o755)
	_ = os.WriteFile(filepath.Join(dir, "module.yaml"), []byte("module: web\nnetwork: {subnet: 10.90.90.0/24}\nvms:\n  srv: {ip: 10}\n"), 0o644)
	for _, c := range []struct {
		body string
		want int
	}{
		{`{"op":"alias-add","module":"web","name":"www","target":"srv"}`, http.StatusNoContent},
		{`{"op":"alias-add","module":"web","name":"a b","target":"srv"}`, http.StatusBadRequest},
		{`{"op":"alias-add","module":"web","name":"x\ninjecte","target":"srv"}`, http.StatusBadRequest},
		{`{"op":"explose","module":"web"}`, http.StatusBadRequest},
	} {
		if rec := call(h, "POST", "/api/config", s.host, s.token, c.body); rec.Code != c.want {
			t.Errorf("%s : %d, attendu %d (%s)", c.body, rec.Code, c.want, strings.TrimSpace(rec.Body.String()))
		}
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "module.yaml")); !strings.Contains(string(b), "www: srv") || strings.Contains(string(b), "injecte") {
		t.Errorf("module.yaml après édition :\n%s", b)
	}
	s.busy.Lock()
	defer s.busy.Unlock()
	if rec := call(h, "POST", "/api/config", s.host, s.token, `{"op":"alias-del","module":"web","name":"www"}`); rec.Code != http.StatusConflict {
		t.Errorf("édition pendant une action : %d", rec.Code)
	}
}
