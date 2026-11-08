package lab

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"labs/labctl/internal/coreconf"
	"labs/labctl/internal/virt"
	"labs/labctl/internal/virt/virtfake"
)

// clearLab : deux modules (web avec une VM, vide avec seulement son réseau), la passerelle branchée, une VM faite à
// la main (perso) dont le disque s'appuie sur l'image debian13, une PKI, et le dossier des images : catalogue,
// disque éditeur converti, cache, et un ISO déposé à la main.
func clearLab(t *testing.T) (*Lab, *virtfake.Fake, string) {
	t.Helper()
	l, f := engineLab(t, map[string]string{"web": webYAML, "vide": "module: vide\nnetwork: {subnet: 10.91.91.0/24}\nvms:\n  a: {ip: 10, source: {url: \"https://example.org/a.qcow2\", checksum: \"sha256:" + strings.Repeat("0", 64) + "\"}}\n"})
	images := filepath.Join(l.Root, "images")
	t.Setenv("LABS_IMAGES_DIR", images)
	isolatedNet(f, netName(t, l, "web"), "10.90.90", true)
	isolatedNet(f, netName(t, l, "vide"), "10.91.91", true)
	vmOf(t, l, f, "web", "srv", virt.StateRunning)
	f.AddVM(virtfake.VM{Name: GatewayVM, State: "shut off", Desc: LabMarker,
		Ifaces: []virt.Interface{{Type: "network", Network: netName(t, l, "web"), MAC: coreconf.GatewayMAC("10.90.90")}}})
	f.AddVM(virtfake.VM{Name: "perso", State: "shut off", Desc: "faite à la main"})
	// la passerelle s'appuie aussi sur debian13 : elle part avec le reste, elle ne la retient pas
	_ = os.MkdirAll(filepath.Join(l.Root, "vms", GatewayVM), 0o755)
	_ = os.WriteFile(filepath.Join(l.Root, "vms", GatewayVM, "disk.qcow2"), []byte("x"), 0o600)
	for _, p := range []string{"pki/root/root-ca.key", "images/debian13.qcow2", "images/ubuntu2404.qcow2",
		"images/sources/vide-a.qcow2", "images/sources/web-srv.qcow2", "images/sources/cache/archive.7z", "images/sources/Win11.iso", "vms/perso/disk.qcow2"} {
		full := filepath.Join(l.Root, p)
		_ = os.MkdirAll(filepath.Dir(full), 0o755)
		if err := os.WriteFile(full, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	base := l.ImagePath("debian13")
	l.Tool = func(name string, args ...string) ([]byte, []byte, error) { // qemu-img info : perso s'appuie sur debian13
		if a := args[len(args)-1]; strings.HasSuffix(a, filepath.Join("perso", "disk.qcow2")) || strings.HasSuffix(a, filepath.Join(GatewayVM, "disk.qcow2")) {
			return []byte(`{"format":"qcow2","full-backing-filename":"` + base + `"}`), nil, nil
		}
		return []byte(`{"format":"qcow2"}`), nil, nil
	}
	return l, f, images
}

func exists(p string) bool { _, err := os.Lstat(p); return err == nil }

// clear --pki --images : modules (VMs et réseaux), passerelle, PKI et téléchargements partent ; restent la VM faite
// à la main et l'image qui lui sert de base, l'ISO déposé à la main, les module.yaml.
func TestClearAll(t *testing.T) {
	l, f, images := clearLab(t)
	p, err := l.PlanClear(ClearOptions{PKI: true, Images: true})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(p.Modules, []string{"vide", "web"}) || !slices.Equal(p.VMs, []string{"web-srv"}) || !p.Gateway || p.PKIDir == "" {
		t.Fatalf("plan : %+v", p)
	}
	if len(p.Kept) != 1 || !strings.HasSuffix(p.Kept[0], "debian13.qcow2 (base de perso)") { // la passerelle supprimée ne compte pas
		t.Errorf("image de base d'une VM qui reste : %q", p.Kept)
	}
	var out bytes.Buffer
	if err := l.Clear(p, &out); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	for _, gone := range []string{"web-srv", GatewayVM} {
		if f.VM(gone) != nil {
			t.Errorf("%s encore là", gone)
		}
	}
	if f.Network(netName(t, l, "web")) != nil || f.Network(netName(t, l, "vide")) != nil {
		t.Error("réseau de module encore là")
	}
	if f.VM("perso") == nil {
		t.Error("VM faite à la main supprimée")
	}
	for _, gone := range []string{filepath.Join(l.Root, "pki"), filepath.Join(images, "ubuntu2404.qcow2"),
		filepath.Join(images, "sources", "vide-a.qcow2"), filepath.Join(images, "sources", "cache")} {
		if exists(gone) {
			t.Errorf("%s encore là", gone)
		}
	}
	for _, kept := range []string{filepath.Join(images, "debian13.qcow2"), filepath.Join(images, "sources", "Win11.iso"),
		filepath.Join(images, "sources", "web-srv.qcow2"), // pas un disque éditeur déclaré : pas à labctl
		filepath.Join(l.Root, "modules", "web", "module.yaml")} {
		if !exists(kept) {
			t.Errorf("%s supprimé", kept)
		}
	}
	if again, _ := l.PlanClear(ClearOptions{PKI: true, Images: true}); !again.Empty() {
		t.Errorf("lab pas vide après clear : %+v", again)
	}
}

// Sans option, PKI et téléchargements restent ; si une VM de module ne peut pas être supprimée (pas créée par
// labctl), ils restent aussi, même demandés : des VMs restantes peuvent en dépendre.
func TestClearKeepsPKIAndImages(t *testing.T) {
	l, _, images := clearLab(t)
	p, _ := l.PlanClear(ClearOptions{})
	if err := l.Clear(p, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if !exists(filepath.Join(l.Root, "pki")) || !exists(filepath.Join(images, "ubuntu2404.qcow2")) {
		t.Fatal("PKI ou images supprimées sans --pki / --images")
	}

	l, f, images := clearLab(t)
	d := f.VM("web-srv")
	d.Desc = "faite à la main"
	f.AddVM(*d)
	p, _ = l.PlanClear(ClearOptions{PKI: true, Images: true})
	if err := l.Clear(p, &bytes.Buffer{}); err == nil {
		t.Fatal("VM étrangère supprimée sans erreur")
	}
	if !exists(filepath.Join(l.Root, "pki")) || !exists(filepath.Join(images, "sources", "vide-a.qcow2")) {
		t.Error("PKI ou disque source supprimés alors qu'une VM du module reste")
	}
}

// LABS_PKI_DIR qui ne désigne pas une PKI : jamais supprimé.
func TestClearPKIDirMustBeAPKI(t *testing.T) {
	l, _, _ := clearLab(t)
	other := t.TempDir()
	_ = os.WriteFile(filepath.Join(other, "precieux.txt"), []byte("x"), 0o600)
	t.Setenv("LABS_PKI_DIR", other)
	if p, _ := l.PlanClear(ClearOptions{PKI: true}); p.PKIDir != "" {
		t.Errorf("dossier qui n'est pas une PKI prévu à la suppression : %s", p.PKIDir)
	}
}
