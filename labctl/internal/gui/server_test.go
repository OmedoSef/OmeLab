package gui

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"labs/labctl/internal/lab"
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
	return &server{lab: &lab.Lab{Root: root}, token: "jeton-de-test"}
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
