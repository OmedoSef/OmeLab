package module

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSourcePath(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "mod")
	if err := os.MkdirAll(filepath.Join(dir, "files"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "files", "ok.conf"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(root, "secret.key") // hors du module : ne doit jamais partir dans une VM
	if err := os.WriteFile(secret, []byte("PRIVATE KEY"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(dir, "files", "lien.key")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(root, filepath.Join(dir, "sortie")); err != nil {
		t.Fatal(err)
	}

	for _, bad := range []string{
		secret, "/etc/passwd", "~/.ssh/id_ed25519", "../secret.key", "files/../../secret.key",
		`..\secret.key`, "C:/Windows/x", "", "files/lien.key", "sortie/secret.key",
	} {
		if p, err := SourcePath(dir, bad); err == nil {
			t.Errorf("src %q accepté (-> %s)", bad, p)
		}
	}
	p, err := SourcePath(dir, "files/ok.conf")
	if err != nil || p != filepath.Join(dir, "files", "ok.conf") {
		t.Fatalf("src légitime refusé : %q %v", p, err)
	}

	// Le dossier du module peut lui-même être un lien (module développé ailleurs)
	linked := filepath.Join(root, "modules-lien")
	if err := os.Symlink(dir, linked); err != nil {
		t.Fatal(err)
	}
	if _, err := SourcePath(linked, "files/ok.conf"); err != nil {
		t.Fatalf("dossier de module relié refusé : %v", err)
	}
}

// Bout en bout : la validation d'un module.yaml signale un src qui vise un fichier de l'hôte.
func TestValidateRejectsHostFileSource(t *testing.T) {
	dir := t.TempDir()
	src := "module: m\nvms:\n  srv:\n    files:\n      - {src: /etc/passwd, dest: /tmp/x}\n      - {src: ../../pki/root/root-ca.key, dest: /tmp/y}\n"
	m, err := Parse([]byte(src), dir)
	if err != nil {
		t.Fatal(err)
	}
	m.Module = filepath.Base(dir)
	var found int
	for _, i := range m.Validate(Context{RootDomain: "omelab.internal"}) {
		if i.Error && strings.HasSuffix(i.Path, ".src") {
			found++
		}
	}
	if found != 2 {
		t.Fatalf("attendu 2 erreurs sur files.src, obtenu %d", found)
	}
}

// labctl ne télécharge jamais d'ISO, ni rien depuis Microsoft.
func TestForbiddenSource(t *testing.T) {
	for _, s := range []*Source{
		{URL: "https://software-download.microsoft.com/x/Win11.iso", Checksum: "sha256:00"},
		{URL: "https://go.microsoft.com/fwlink/?linkid=1", Checksum: "sha256:00"},
		{URL: "https://example.org/win.ISO", Checksum: "sha256:00"},
		{Sums: "https://download.windowsupdate.com/sums", File: "x.qcow2"},
		{Sums: "https://example.org/SHA256SUMS", File: "windows.iso"},
		{URL: "https://example.org/a.zip", Checksum: "sha256:00", Extract: "*.iso"},
	} {
		if err := ForbiddenSource(s); err == nil {
			t.Errorf("source acceptée : %+v", s)
		}
	}
	for _, s := range []*Source{
		nil,
		{URL: "https://sourceforge.net/projects/metasploitable/files/metasploitable-linux-2.0.0.zip", Checksum: "md5:00", Extract: "*.vmdk"},
		{Sums: "https://cdimage.kali.org/current/SHA256SUMS", File: "kali-linux-*-qemu-amd64.7z", Extract: "*.qcow2"},
		{URL: "https://notmicrosoft.com.example.org/disk.qcow2", Checksum: "sha256:00"},
	} {
		if err := ForbiddenSource(s); err != nil {
			t.Errorf("source légitime refusée : %+v : %v", s, err)
		}
	}
}

// Le nom de fichier retenu dans un fichier d'empreintes (file: avec *) et chaque hôte de redirection sont contrôlés.
func TestForbiddenNameAndHost(t *testing.T) {
	if ForbiddenName("Win11_x64.ISO") == nil || ForbiddenName("disk.qcow2") != nil {
		t.Error("ForbiddenName")
	}
	if MicrosoftHost("download.MICROSOFT.com") == nil || MicrosoftHost("cdimage.debian.org") != nil {
		t.Error("MicrosoftHost")
	}
}
