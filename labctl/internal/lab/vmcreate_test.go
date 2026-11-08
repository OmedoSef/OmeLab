package lab

import (
	"bytes"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"labs/labctl/internal/virt"
	"labs/labctl/internal/virt/virtfake"
)

// fakeTools remplace qemu-img et cloud-localds (Lab.Tool) : note chaque appel et crée le fichier produit.
type fakeTools struct {
	calls [][]string
	fail  string // outil qui échoue
}

func (f *fakeTools) run(name string, args ...string) ([]byte, error) {
	f.calls = append(f.calls, append([]string{name}, args...))
	if name == f.fail {
		return []byte(name + " : panne simulée"), errors.New("exit status 1")
	}
	var out string
	switch name {
	case "qemu-img": // create … <disque> [taille] : le disque est l'avant-dernier ou le dernier argument
		for _, a := range args {
			if strings.HasSuffix(a, ".qcow2") && !slices.Contains(args[:max(0, slices.Index(args, "-b")+2)], a) {
				out = a
			}
		}
	case "cloud-localds": // -N network-config <seed> user-data meta-data
		out = args[2]
	}
	if out != "" {
		_ = os.WriteFile(out, nil, 0o644)
	}
	return nil, nil
}

// createLab : lab de test avec un réseau NAT, une image de base qcow2 réelle (contrôlée par qemu-img info) et une
// clé SSH ; outils et SSH remplacés.
func createLab(t *testing.T) (*Lab, *virtfake.Fake, *fakeTools) {
	t.Helper()
	if _, err := exec.LookPath("qemu-img"); err != nil {
		t.Skip("qemu-img absent (contrôle de l'image de base)")
	}
	l, f := engineLab(t, nil)
	t.Setenv("LABS_IMAGES_DIR", filepath.Join(l.Root, "images"))
	t.Setenv("LABS_SSH_PUBKEY", filepath.Join(l.Root, "id.pub"))
	t.Setenv("LABS_KEYBOARD", "fr")
	if err := os.WriteFile(filepath.Join(l.Root, "id.pub"), []byte("ssh-ed25519 AAAA test\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(l.ImagesDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if b, err := exec.Command("qemu-img", "create", "-q", "-f", "qcow2", l.ImagePath("debian13"), "1M").CombinedOutput(); err != nil {
		t.Fatalf("qemu-img : %s", b)
	}
	f.AddNetwork(virt.Network{Name: "demo", Forward: "nat", HostIP: "10.10.0.1", Prefix: 24, Active: true})
	f.IPs = map[string]string{"web1": "10.10.0.11"}
	tools := &fakeTools{}
	l.Tool = tools.run
	l.Shell = func(_, script string, _ io.Reader) ([]byte, []byte, error) {
		if strings.Contains(script, "cloud-init status") {
			return []byte("status: done\n"), nil, nil
		}
		return nil, nil, nil
	}
	return l, f, tools
}

func spec() VMSpec {
	return VMSpec{Name: "web1", Image: "debian13", CPUs: 2, MemoryMiB: 1024, DiskGiB: 10, Lab: "demo",
		Nets: []NetSpec{{Network: "demo", Address: "10.10.0.11/24", MAC: "52:54:00:12:34:56"}}}
}

// Création complète : disque lié à l'image de base et agrandi, disque cloud-init, définition libvirt (carte, MAC,
// marque labctl), démarrage, attente de cloud-init.
func TestCreateVM(t *testing.T) {
	l, f, tools := createLab(t)
	var out bytes.Buffer
	if err := l.CreateVM(spec(), CreateOptions{Wait: true, Out: &out}); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	dir := filepath.Join(l.VMDir(), "web1")
	want := []string{"qemu-img", "create", "-q", "-f", "qcow2", "-F", "qcow2", "-b", l.ImagePath("debian13"), filepath.Join(dir, "disk.qcow2"), "10G"}
	if len(tools.calls) != 2 || !slices.Equal(tools.calls[0], want) || tools.calls[1][0] != "cloud-localds" {
		t.Fatalf("outils appelés :\n%q", tools.calls)
	}
	ud, err := os.ReadFile(filepath.Join(dir, "user-data"))
	if err != nil || !strings.Contains(string(ud), "ssh-ed25519 AAAA test") {
		t.Errorf("user-data sans la clé SSH : %v", err)
	}
	if fi, _ := os.Stat(filepath.Join(dir, "user-data")); fi == nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("user-data lisible par d'autres : %v", fi)
	}
	vm := f.VM("web1")
	if vm == nil || vm.State != virt.StateRunning || vm.CPUs != 2 || vm.MemMiB != 1024 || !strings.Contains(vm.Desc, LabMarker) {
		t.Fatalf("VM définie : %+v", vm)
	}
	if len(vm.Ifaces) != 1 || vm.Ifaces[0].Network != "demo" || vm.Ifaces[0].MAC != "52:54:00:12:34:56" {
		t.Errorf("cartes : %+v", vm.Ifaces)
	}
	if !strings.Contains(out.String(), "status: done") || !strings.Contains(out.String(), "web1 prête") {
		t.Errorf("attente de cloud-init :\n%s", out.String())
	}
}

// Une création ratée ne laisse rien : ni définition libvirt, ni dossier de la VM.
func TestCreateVMRollsBack(t *testing.T) {
	for _, step := range []string{"qemu-img", "cloud-localds", "start"} {
		t.Run(step, func(t *testing.T) {
			l, f, tools := createLab(t)
			if step == "start" {
				f.Fail = map[string]error{"start web1": errors.New("libvirt : démarrage impossible")}
			} else {
				tools.fail = step
			}
			err := l.CreateVM(spec(), CreateOptions{Out: io.Discard})
			if err == nil {
				t.Fatal("création réussie malgré la panne")
			}
			if f.VM("web1") != nil {
				t.Errorf("définition libvirt restée après l'échec (%v)", err)
			}
			if _, serr := os.Stat(filepath.Join(l.VMDir(), "web1")); !os.IsNotExist(serr) {
				t.Errorf("dossier de la VM resté après l'échec (%v)", err)
			}
		})
	}
}

// Refus avant toute action : VM existante, réseau inconnu, image de base piégée.
func TestCreateVMRefusals(t *testing.T) {
	l, f, tools := createLab(t)
	f.AddVM(virtfake.VM{Name: "web1", State: "shut off"})
	if err := l.CreateVM(spec(), CreateOptions{}); err == nil || !strings.Contains(err.Error(), "existe déjà") {
		t.Errorf("VM existante : %v", err)
	}
	s := spec()
	s.Name, s.Nets[0].Network = "web2", "inconnu"
	if err := l.CreateVM(s, CreateOptions{}); err == nil || !strings.Contains(err.Error(), "réseau libvirt inexistant") {
		t.Errorf("réseau inconnu : %v", err)
	}
	// Image de base qui dépend d'un fichier de l'hôte : refusée avant de créer le disque
	secret := filepath.Join(l.Root, "secret.raw")
	_ = os.WriteFile(secret, make([]byte, 1<<20), 0o600)
	_ = os.Remove(l.ImagePath("debian13"))
	if b, err := exec.Command("qemu-img", "create", "-q", "-f", "qcow2", "-F", "raw", "-b", secret, l.ImagePath("debian13")).CombinedOutput(); err != nil {
		t.Fatalf("qemu-img : %s", b)
	}
	s.Name, s.Nets[0].Network = "web3", "demo"
	if err := l.CreateVM(s, CreateOptions{}); err == nil || !strings.Contains(err.Error(), "refusée") {
		t.Errorf("image de base piégée : %v", err)
	}
	if len(tools.calls) != 0 {
		t.Errorf("outils lancés malgré les refus : %q", tools.calls)
	}
}
