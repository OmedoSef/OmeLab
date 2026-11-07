package gui

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"labs/labctl/internal/lab"
	"labs/labctl/internal/virt/virtfake"
)

func testServer(t *testing.T) *server {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "config.env"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "modules"), 0o755); err != nil {
		t.Fatal(err)
	}
	return &server{lab: &lab.Lab{Root: root, Virt: virtfake.New()}, token: "jeton-de-test"}
}

// Toute l'API exige le jeton (comparé à temps constant).
func TestAPIRequiresToken(t *testing.T) {
	s := testServer(t)
	called := false
	h := s.api(func(http.ResponseWriter, *http.Request) { called = true })
	for _, tok := range []string{"", "mauvais", "jeton-de-test-et-plus"} {
		req := httptest.NewRequest("GET", "/api/state", nil)
		req.Header.Set("X-Lab-Token", tok)
		rec := httptest.NewRecorder()
		h(rec, req)
		if rec.Code != http.StatusUnauthorized || called {
			t.Errorf("jeton %q : code %d, handler appelé %v", tok, rec.Code, called)
		}
	}
	req := httptest.NewRequest("GET", "/api/state", nil)
	req.Header.Set("X-Lab-Token", "jeton-de-test")
	h(httptest.NewRecorder(), req)
	if !called {
		t.Error("le bon jeton devrait passer")
	}
}

// /api/run refuse avant d'exécuter quoi que ce soit : commandes réservées au terminal ou hors liste, trust-host
// même précédé d'une option, arguments piégés.
func TestRunRefusals(t *testing.T) {
	s := testServer(t)
	for _, body := range []string{
		`{"args":["gui"]}`,
		`{"args":["init","/tmp/x"]}`,
		`{"args":["completion","bash"]}`,
		`{"args":["gw-portal","serve"]}`,
		`{"args":["pki","trust-host"]}`,
		`{"args":["pki","-v","trust-host"]}`,
		`{"args":["pki","--verbose","trust-host"]}`,
		`{"args":["inconnu"]}`,
		`{"args":["status","a\nb"]}`,
		`{"args":[]}`,
		`pas du json`,
	} {
		rec := httptest.NewRecorder()
		s.handleRun(rec, httptest.NewRequest("POST", "/api/run", strings.NewReader(body)))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s : code %d, refus attendu (400)", body, rec.Code)
		}
	}
}

// Une action de l'interface est journalisée (route, paramètres, résultat) sans contenu de fichier ni mot de passe,
// et le gestionnaire reçoit la requête intacte.
func TestAuditedGUIAction(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "audit.jsonl")
	t.Setenv("LABCTL_AUDIT", logPath)
	s := testServer(t)
	body := `{"op":"conn-add","module":"netlab","target":"internet","portal":{"auth":"password","password":"S3cret!"},"text":"contenu du fichier"}`
	var got string
	h := s.audited(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got = string(b)
		http.Error(w, "refusé", http.StatusBadRequest)
	})
	h(httptest.NewRecorder(), httptest.NewRequest("POST", "/api/config", strings.NewReader(body)))
	if got != body {
		t.Fatalf("corps modifié pour le gestionnaire : %q", got)
	}
	b, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	line := string(b)
	for _, want := range []string{`"event":"gui"`, `"action":"POST /api/config"`, `op=conn-add`, `portal.auth=password`, `"ok":false`} {
		if !strings.Contains(line, want) {
			t.Errorf("journal sans %s : %s", want, line)
		}
	}
	for _, secret := range []string{"S3cret!", "contenu du fichier"} {
		if strings.Contains(line, secret) {
			t.Errorf("secret ou contenu dans le journal : %s", line)
		}
	}
}
