package lab

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPublicFileNeverServesKeys(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("LABS_PKI_DIR", dir)
	os.MkdirAll(filepath.Join(dir, "certs", "web"), 0o755)
	os.WriteFile(filepath.Join(dir, "certs", "web", "web.crt"), []byte("-----BEGIN CERTIFICATE-----\nx\n-----END CERTIFICATE-----\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "certs", "web", "web.key"), []byte("-----BEGIN PRIVATE KEY-----\nx\n"), 0o600)
	// un .crt qui contiendrait aussi une clé (erreur de manipulation) ne doit pas sortir non plus
	os.WriteFile(filepath.Join(dir, "certs", "web", "bundle.crt"), []byte("-----BEGIN CERTIFICATE-----\n-----BEGIN RSA PRIVATE KEY-----\n"), 0o644)
	os.WriteFile(filepath.Join(filepath.Dir(dir), "outside.crt"), []byte("-----BEGIN CERTIFICATE-----\n"), 0o644)

	l := &Lab{Root: t.TempDir()}
	if _, err := l.PublicFile("certs/web/web.crt"); err != nil {
		t.Errorf("certificat public refusé : %v", err)
	}
	for _, bad := range []string{
		"certs/web/web.key", "certs/web/bundle.crt", "../outside.crt", "certs/../../outside.crt",
		"/etc/passwd", "certs/web/web.p12", "root/root-ca.key",
	} {
		if _, err := l.PublicFile(bad); err == nil {
			t.Errorf("%s ne doit pas être servi", bad)
		}
	}
}

func TestCompletePKI(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("LABS_PKI_DIR", dir)
	os.MkdirAll(filepath.Join(dir, "sub-ca", "pentest"), 0o755)
	os.MkdirAll(filepath.Join(dir, "certs", "web"), 0o755)
	l := &Lab{Root: t.TempDir()}
	check := func(typed []string, cur string, want ...string) {
		t.Helper()
		got := l.CompletePKI(typed, cur)
		if len(got) != len(want) {
			t.Fatalf("CompletePKI(%v, %q) = %v, attendu %v", typed, cur, got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("CompletePKI(%v, %q) = %v, attendu %v", typed, cur, got, want)
			}
		}
	}
	check(nil, "s", "show", "sub-ca")
	check([]string{"issue", "srv", "-C"}, "", "pentest")
	check([]string{"issue", "srv", "-d"}, "") // valeur libre
	check([]string{"show"}, "", "web")
	check([]string{"issue", "srv", "--pfx", "-C", "pentest"}, "--", "--client", "--days")
}
