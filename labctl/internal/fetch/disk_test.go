package fetch

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"labs/labctl/internal/testtool"
)

func TestCheckDiskInfoJSON(t *testing.T) {
	for _, c := range []struct {
		name, json string
		ok         bool
	}{
		{"qcow2 sain", `{"format":"qcow2"}`, true},
		{"raw", `{"format":"raw"}`, true},
		{"fichier de base", `{"format":"qcow2","backing-filename":"/home/u/.ssh/id_ed25519"}`, false},
		{"données externes", `{"format":"qcow2","format-specific":{"type":"qcow2","data":{"data-file":"/etc/shadow"}}}`, false},
		{"extent ailleurs", `{"format":"vmdk","format-specific":{"type":"vmdk","data":{"extents":[{"filename":"/home/u/.ssh/id_ed25519"}]}}}`, false},
		{"extent à côté", `{"format":"vmdk","format-specific":{"type":"vmdk","data":{"extents":[{"filename":"/tmp/x/disk-s001.vmdk"}]}}}`, true},
		{"format inattendu", `{"format":"luks"}`, false},
		{"pas du JSON", `oups`, false},
	} {
		_, err := checkDiskInfo("/tmp/x/disk.vmdk", []byte(c.json))
		if (err == nil) != c.ok {
			t.Errorf("%s : erreur=%v, accepté attendu=%v", c.name, err, c.ok)
		}
	}
}

// Vraies images piégées, fabriquées avec qemu-img : un fichier « secret » de l'hôte ne doit jamais pouvoir être
// référencé par une image.
func TestCheckDiskRealImages(t *testing.T) {
	testtool.Need(t, "qemu-img")
	root := t.TempDir()
	hostDir, imgDir := filepath.Join(root, "hote"), filepath.Join(root, "images")
	for _, d := range []string{hostDir, imgDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	secret := filepath.Join(hostDir, "secret.raw")
	if err := os.WriteFile(secret, make([]byte, 1<<20), 0o600); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) {
		t.Helper()
		if b, err := exec.Command("qemu-img", args...).CombinedOutput(); err != nil {
			t.Fatalf("qemu-img %v : %s", args, b)
		}
	}
	sain := filepath.Join(imgDir, "sain.qcow2")
	run("create", "-q", "-f", "qcow2", sain, "1M")
	base := filepath.Join(imgDir, "piege-base.qcow2")
	run("create", "-q", "-f", "qcow2", "-F", "raw", "-b", secret, base)
	data := filepath.Join(imgDir, "piege-data.qcow2")
	run("create", "-q", "-f", "qcow2", "-o", "data_file="+secret+",data_file_raw=on", data, "1M")
	vmdk := filepath.Join(imgDir, "piege.vmdk") // descripteur VMDK dont l'extent est le secret
	desc := "# Disk DescriptorFile\nversion=1\nCID=fffffffe\nparentCID=ffffffff\ncreateType=\"monolithicFlat\"\n\nRW 2048 FLAT \"" + secret + "\" 0\n"
	if err := os.WriteFile(vmdk, []byte(desc), 0o644); err != nil {
		t.Fatal(err)
	}

	if f, err := CheckDisk(sain); err != nil || f != "qcow2" {
		t.Fatalf("image saine refusée : %q %v", f, err)
	}
	for _, p := range []string{base, data, vmdk} {
		if _, err := CheckDisk(p); err == nil || !strings.Contains(err.Error(), "refusée") {
			t.Errorf("%s acceptée : %v", filepath.Base(p), err)
		}
	}
}

// Un VMDK dont un morceau (extent) est un lien symbolique posé à côté de lui — vers un fichier de l'hôte — est
// refusé, même si le lien est dans le dossier de l'image (seul cas que voyait l'ancien contrôle).
func TestCheckDiskRefusesSymlinkedExtent(t *testing.T) {
	testtool.Need(t, "qemu-img")
	root := t.TempDir()
	secret := filepath.Join(root, "secret.raw")
	if err := os.WriteFile(secret, make([]byte, 1<<20), 0o600); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "archive")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "boot.img"), make([]byte, 1<<20), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../secret.raw", filepath.Join(dir, "x.bin")); err != nil {
		t.Fatal(err)
	}
	vmdk := filepath.Join(dir, "disk.vmdk")
	desc := "# Disk DescriptorFile\nversion=1\nCID=fffffffe\nparentCID=ffffffff\ncreateType=\"monolithicFlat\"\n\n" +
		"RW 2048 FLAT \"boot.img\" 0\nRW 2048 FLAT \"x.bin\" 0\n"
	if err := os.WriteFile(vmdk, []byte(desc), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := CheckDisk(vmdk); err == nil || !strings.Contains(err.Error(), "fichier ordinaire") {
		t.Fatalf("extent en lien symbolique accepté : %v", err)
	}
	// Le même VMDK avec des morceaux ordinaires passe
	if err := os.Remove(filepath.Join(dir, "x.bin")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "x.bin"), make([]byte, 1<<20), 0o644); err != nil {
		t.Fatal(err)
	}
	if f, err := CheckDisk(vmdk); err != nil || f != "vmdk" {
		t.Errorf("VMDK sain refusé : %q %v", f, err)
	}
}
