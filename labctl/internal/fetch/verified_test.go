package fetch

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"labs/labctl/internal/testtool"
)

func sum256(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

// serve sert content sous /f (requêtes Range comprises) et compte les requêtes.
func serve(t *testing.T, content []byte) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		http.ServeContent(w, r, "f", time.Unix(0, 0), bytes.NewReader(content))
	}))
	t.Cleanup(srv.Close)
	return srv, &n
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

// Un fichier n'est publié que vérifié ; une empreinte fausse ne laisse ni fichier ni partiel.
func TestVerified(t *testing.T) {
	content := bytes.Repeat([]byte("labctl"), 50000)
	srv, _ := serve(t, content)
	dir := t.TempDir()

	dst := filepath.Join(dir, "bon.bin")
	if err := Verified(srv.URL+"/f", dst, "sha256", sum256(content), nil, io.Discard); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(dst); !bytes.Equal(b, content) || exists(dst+".part") {
		t.Fatal("fichier publié incorrect, ou partiel laissé")
	}

	bad := filepath.Join(dir, "faux.bin")
	err := Verified(srv.URL+"/f", bad, "sha256", strings.Repeat("0", 64), nil, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "empreinte sha256 invalide") || exists(bad) || exists(bad+".part") {
		t.Fatalf("empreinte fausse : %v (publié %v, partiel %v)", err, exists(bad), exists(bad+".part"))
	}

	refused := filepath.Join(dir, "refuse.bin")
	err = Verified(srv.URL+"/f", refused, "sha256", sum256(content), func(string) error { return errors.New("structure refusée") }, io.Discard)
	if err == nil || exists(refused) || exists(refused+".part") {
		t.Fatalf("contrôle refusé : %v (publié %v, partiel %v)", err, exists(refused), exists(refused+".part"))
	}
}

// Un téléchargement interrompu reprend où il s'était arrêté (requête Range) et le résultat est vérifié.
func TestVerifiedResumes(t *testing.T) {
	content := bytes.Repeat([]byte("0123456789"), 30000)
	srv, _ := serve(t, content)
	dst := filepath.Join(t.TempDir(), "repris.bin")
	if err := os.WriteFile(dst+".part", content[:100000], 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Verified(srv.URL+"/f", dst, "sha256", sum256(content), nil, io.Discard); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(dst); !bytes.Equal(b, content) {
		t.Fatalf("fichier repris : %d octets sur %d", len(b), len(content))
	}
}

// Cached ne retélécharge pas un fichier intact, mais remplace un fichier altéré.
func TestCached(t *testing.T) {
	content := []byte("archive de test")
	srv, n := serve(t, content)
	dst := filepath.Join(t.TempDir(), "cache", "a.tar.gz")
	for range 2 {
		if err := Cached(srv.URL+"/f", dst, "sha256", sum256(content), "a", io.Discard); err != nil {
			t.Fatal(err)
		}
	}
	if n.Load() != 1 {
		t.Fatalf("%d téléchargements pour un fichier déjà en cache", n.Load())
	}
	if err := os.WriteFile(dst, []byte("altéré"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Cached(srv.URL+"/f", dst, "sha256", sum256(content), "a", io.Discard); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(dst); !bytes.Equal(b, content) {
		t.Fatal("fichier altéré non remplacé")
	}
}

// Une redirection vers une adresse http:// est refusée (l'empreinte ne doit pas pouvoir être réécrite en chemin).
func TestDownloadRefusesHTTPRedirect(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://example.invalid/SHA256SUMS", http.StatusFound)
	}))
	defer srv.Close()
	if _, err := Small(srv.URL); err == nil || !strings.Contains(err.Error(), "https attendu") {
		t.Fatalf("redirection http acceptée : %v", err)
	}
}

func TestNewHashUnknown(t *testing.T) {
	if _, err := NewHash("crc32"); err == nil {
		t.Error("algorithme inconnu accepté")
	}
	if _, err := FileHash(os.DevNull, "sha3"); err == nil {
		t.Error("FileHash avec un algorithme inconnu")
	}
}

// Image du catalogue de bout en bout : empreinte lue avant, image vérifiée puis publiée ; une image dont
// l'empreinte publiée ne correspond pas n'est jamais publiée.
func TestFetchImage(t *testing.T) {
	testtool.Need(t, "qemu-img")
	src := filepath.Join(t.TempDir(), "img.qcow2")
	if b, err := exec.Command("qemu-img", "create", "-q", "-f", "qcow2", src, "1M").CombinedOutput(); err != nil {
		t.Fatalf("qemu-img : %s", b)
	}
	img, _ := os.ReadFile(src)
	sums := sum256(img) + "  test.qcow2\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/SHA256SUMS":
			io.WriteString(w, sums)
		case "/test.qcow2":
			http.ServeContent(w, r, "test.qcow2", time.Unix(0, 0), bytes.NewReader(img))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	Images["test-image"] = Image{URL: srv.URL + "/test.qcow2", Sums: srv.URL + "/SHA256SUMS", Algo: "sha256"}
	defer delete(Images, "test-image")

	dst := filepath.Join(t.TempDir(), "test-image.qcow2")
	if err := FetchImage("test-image", dst, false, io.Discard); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(dst); err != nil || fi.Mode().Perm() != 0o644 {
		t.Fatalf("image publiée : %v %v", fi, err)
	}

	sums = strings.Repeat("0", 64) + "  test.qcow2\n"
	bad := filepath.Join(t.TempDir(), "mauvaise.qcow2")
	if err := FetchImage("test-image", bad, false, io.Discard); err == nil || exists(bad) {
		t.Fatalf("image à l'empreinte fausse publiée : %v", err)
	}
}
