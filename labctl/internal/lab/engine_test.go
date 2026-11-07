package lab

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"labs/labctl/internal/coreconf"
	"labs/labctl/internal/module"
	"labs/labctl/internal/virt"
	"labs/labctl/internal/virt/virtfake"
)

// Tests du moteur des modules (up, plan, start/stop, snapshots, destroy, status) sur un faux hyperviseur : ils
// n'exercent que ce qui passe par libvirt ; la création d'une VM (disques, cloud-init, SSH) n'est pas couverte.

// engineLab crée un lab de test avec les modules donnés (nom -> module.yaml), branché sur un faux hyperviseur vide.
func engineLab(t *testing.T, modules map[string]string) (*Lab, *virtfake.Fake) {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "config.env"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	for name, yml := range modules {
		dir := filepath.Join(root, "modules", name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, module.FileName), []byte(yml), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("LABS_PKI_DOMAIN", "omelab.internal")
	t.Setenv("LABS_VM_DIR", filepath.Join(root, "vms"))
	t.Setenv("LABS_PKI_DIR", filepath.Join(root, "pki"))
	f := virtfake.New()
	return &Lab{Root: root, Virt: f}, f
}

// vmOf déclare dans le faux la VM vm du module, conforme à sa description (vCPU, mémoire, carte sur le réseau du
// module), dans l'état state.
func vmOf(t *testing.T, l *Lab, f *virtfake.Fake, mod, vm, state string) *virtfake.VM {
	t.Helper()
	r, err := l.loadRun(mod, nil)
	if err != nil {
		t.Fatal(err)
	}
	e := r.eff(vm)
	mac := e.MAC
	if mac == "" {
		mac = "52:54:00:00:00:01"
	}
	d := virtfake.VM{Name: r.libvirtName(vm), State: state, Desc: LabMarker, CPUs: e.CPUs, MemMiB: e.Memory,
		Ifaces: []virt.Interface{{Type: "network", Network: r.y.NetworkName(), MAC: mac, Model: "virtio"}}}
	f.AddVM(d)
	return &d
}

// isolatedNet déclare le réseau (isolé, à l'adresse attendue) d'un module.
func isolatedNet(f *virtfake.Fake, name, prefix3 string, active bool) {
	f.AddNetwork(virt.Network{Name: name, Bridge: "virbr-x", HostIP: prefix3 + ".1", Prefix: 24, Active: active})
}

func netName(t *testing.T, l *Lab, mod string) string {
	t.Helper()
	r, err := l.loadRun(mod, nil)
	if err != nil {
		t.Fatal(err)
	}
	return r.y.NetworkName()
}

func wantCalls(t *testing.T, f *virtfake.Fake, want ...string) {
	t.Helper()
	if !slices.Equal(f.Calls, want) {
		t.Errorf("opérations libvirt :\n  obtenu  %q\n  attendu %q", f.Calls, want)
	}
}

const webYAML = "module: web\nnetwork: {subnet: 10.90.90.0/24}\nvms:\n  srv: {ip: 10}\n"

// Premier up d'un module dont la VM existe déjà : réseau isolé défini puis démarré, VM reprise sans modification ;
// sans passerelle, l'absence de DHCP/DNS est signalée.
func TestUpDefinesIsolatedNetworkAndAdoptsVM(t *testing.T) {
	l, f := engineLab(t, map[string]string{"web": webYAML})
	vmOf(t, l, f, "web", "srv", "shut off")
	var out bytes.Buffer
	if err := l.ModuleUp("web", nil, UpOptions{Out: &out}); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	net := netName(t, l, "web")
	wantCalls(t, f, "define-net "+net, "start-net "+net)
	n := f.Network(net)
	if n.Forward != "" || n.HostIP != "10.90.90.1" || n.Prefix != 24 || !n.Active || !n.Autostart {
		t.Errorf("réseau créé : %+v", *n)
	}
	for _, want := range []string{"pas de passerelle", "srv existe (shut off"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("sortie sans %q :\n%s", want, out.String())
		}
	}
}

// Sans sous-réseau dans le module.yaml, up choisit le premier /24 libre (ceux des réseaux libvirt existants sont
// évités) et l'écrit dans le fichier.
func TestUpChoosesFreeSubnet(t *testing.T) {
	l, f := engineLab(t, map[string]string{"web": "module: web\nvms:\n  srv: {}\n"})
	f.AddNetwork(virt.Network{Name: "autre", HostIP: "10.67.67.1", Prefix: 24})
	vmOf(t, l, f, "web", "srv", "shut off")
	var out bytes.Buffer
	if err := l.ModuleUp("web", nil, UpOptions{Out: &out}); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	y, err := module.Load(filepath.Join(l.Root, "modules", "web"))
	if err != nil {
		t.Fatal(err)
	}
	if got := y.Subnet().String(); got != "10.68.68.0/24" {
		t.Fatalf("sous-réseau écrit : %s\n%s", got, out.String())
	}
	if n := f.Network(y.NetworkName()); n == nil || n.HostIP != "10.68.68.1" {
		t.Errorf("réseau libvirt : %+v", n)
	}
}

// Un réseau existant du même nom qui n'est pas isolé, ou pas à l'adresse attendue, bloque up sans rien démarrer.
func TestUpRefusesForeignNetwork(t *testing.T) {
	for _, tc := range []struct {
		name string
		net  virt.Network
		want string
	}{
		{"nat", virt.Network{Forward: "nat", HostIP: "10.90.90.1", Prefix: 24}, "PAS isolé"},
		{"adresse", virt.Network{HostIP: "10.91.91.1", Prefix: 24}, "le module attend 10.90.90.1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l, f := engineLab(t, map[string]string{"web": webYAML})
			tc.net.Name = netName(t, l, "web")
			f.AddNetwork(tc.net)
			err := l.ModuleUp("web", nil, UpOptions{})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("erreur %q attendue, obtenu %v", tc.want, err)
			}
			wantCalls(t, f)
		})
	}
}

// Une VM existante dont les ressources diffèrent du YAML : écart signalé ; avec --apply-changes, corrigé et la VM
// (en marche) redémarrée pour les prendre.
func TestUpReportsAndAppliesResourceDiffs(t *testing.T) {
	l, f := engineLab(t, map[string]string{"web": webYAML})
	net := netName(t, l, "web")
	isolatedNet(f, net, "10.90.90", true)
	d := vmOf(t, l, f, "web", "srv", virt.StateRunning)
	d.CPUs, d.MemMiB = d.CPUs+1, d.MemMiB*2
	f.AddVM(*d)
	want := vmOf(t, l, f, "web", "srv", virt.StateRunning) // valeurs du YAML
	f.AddVM(*d)

	var out bytes.Buffer
	if err := l.ModuleUp("web", nil, UpOptions{Out: &out}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "écart srv : cpus") || !strings.Contains(out.String(), "--apply-changes srv") {
		t.Errorf("écart non signalé :\n%s", out.String())
	}
	wantCalls(t, f)

	out.Reset()
	if err := l.ModuleUp("web", nil, UpOptions{Out: &out, ApplyChanges: true}); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	got := f.VM(d.Name)
	if got.CPUs != want.CPUs || got.MemMiB != want.MemMiB || got.State != virt.StateRunning {
		t.Errorf("VM après --apply-changes : %d vCPU %d Mio %s, attendu %d %d running", got.CPUs, got.MemMiB, got.State, want.CPUs, want.MemMiB)
	}
	wantCalls(t, f, fmt.Sprintf("resources %s %d %d", d.Name, want.CPUs, want.MemMiB), "shutdown "+d.Name, "start "+d.Name)
}

// Une carte hors du réseau du module (ici le réseau NAT de libvirt) : la VM n'est pas démarrée.
func TestStartRefusesVMOutsideModuleNetwork(t *testing.T) {
	l, f := engineLab(t, map[string]string{"web": webYAML})
	isolatedNet(f, netName(t, l, "web"), "10.90.90", true)
	d := vmOf(t, l, f, "web", "srv", "shut off")
	d.Ifaces = append(d.Ifaces, virt.Interface{Type: "network", Network: "default", MAC: "52:54:00:00:00:99"})
	f.AddVM(*d)
	err := l.ModuleStart("web", nil, nil)
	if err == nil || !strings.Contains(err.Error(), "carte hors de") {
		t.Fatalf("refus d'isolation attendu, obtenu %v", err)
	}
	if st := f.VM(d.Name).State; st != "shut off" {
		t.Errorf("VM démarrée malgré tout : %s", st)
	}
}

// start démarre le réseau puis les VMs arrêtées ; stop les arrête ; une VM absente est signalée, pas créée.
func TestStartStop(t *testing.T) {
	l, f := engineLab(t, map[string]string{"web": "module: web\nnetwork: {subnet: 10.90.90.0/24}\nvms:\n  srv: {ip: 10}\n  cli: {ip: 11}\n"})
	net := netName(t, l, "web")
	isolatedNet(f, net, "10.90.90", false)
	d := vmOf(t, l, f, "web", "srv", "shut off")
	var out bytes.Buffer
	if err := l.ModuleStart("web", nil, &out); err != nil {
		t.Fatal(err)
	}
	wantCalls(t, f, "start-net "+net, "start "+d.Name)
	if !strings.Contains(out.String(), "cli n'existe pas") {
		t.Errorf("VM absente non signalée :\n%s", out.String())
	}
	f.Calls = nil
	if err := l.ModuleStop("web", nil, false, &out); err != nil {
		t.Fatal(err)
	}
	wantCalls(t, f, "shutdown "+d.Name)
	if st := f.VM(d.Name).State; st != "shut off" {
		t.Errorf("après stop : %s", st)
	}
}

// snapshot : nom par défaut celui de snapshot: (sinon clean), refus d'écraser sans replace ; restore ramène la VM à
// l'état du snapshot.
func TestSnapshotAndRestore(t *testing.T) {
	l, f := engineLab(t, map[string]string{"web": "module: web\nnetwork: {subnet: 10.90.90.0/24}\nvms:\n  srv: {ip: 10, snapshot: base}\n"})
	isolatedNet(f, netName(t, l, "web"), "10.90.90", true)
	d := vmOf(t, l, f, "web", "srv", "shut off")
	if err := l.ModuleSnapshot("web", "srv", "", false, nil); err != nil {
		t.Fatal(err)
	}
	if got := f.SnapshotNames(d.Name); !slices.Equal(got, []string{"base"}) {
		t.Fatalf("snapshots : %v", got)
	}
	if err := l.ModuleSnapshot("web", "srv", "base", false, nil); err == nil || !strings.Contains(err.Error(), "existe déjà") {
		t.Errorf("écrasement sans replace : %v", err)
	}
	if err := l.ModuleSnapshot("web", "srv", "base", true, nil); err != nil {
		t.Errorf("replace : %v", err)
	}
	if err := l.ModuleRestore("web", "srv", "inconnu", nil); err == nil || !strings.Contains(err.Error(), "pas de snapshot inconnu") {
		t.Errorf("restore d'un snapshot inconnu : %v", err)
	}
	if err := l.ModuleStart("web", nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := l.ModuleRestore("web", "srv", "", nil); err != nil {
		t.Fatal(err)
	}
	if st := f.VM(d.Name).State; st != "shut off" {
		t.Errorf("après restore (snapshot pris VM arrêtée) : %s", st)
	}
}

// UEFI : libvirt ne prend le snapshot interne que VM arrêtée ; la VM est arrêtée, photographiée puis redémarrée.
func TestSnapshotEFIStopsRunningVM(t *testing.T) {
	l, f := engineLab(t, map[string]string{"web": webYAML})
	d := vmOf(t, l, f, "web", "srv", virt.StateRunning)
	d.EFI = true
	f.AddVM(*d)
	if err := l.ModuleSnapshot("web", "srv", "", false, nil); err != nil {
		t.Fatal(err)
	}
	wantCalls(t, f, "shutdown "+d.Name, "snapshot "+d.Name+" clean", "start "+d.Name)
}

// destroy sans VM : VMs supprimées, passerelle débranchée, réseau supprimé ; un module dont une VM n'a pas été
// créée par labctl est refusé et garde son réseau.
func TestDestroyModule(t *testing.T) {
	l, f := engineLab(t, map[string]string{"web": webYAML})
	net := netName(t, l, "web")
	isolatedNet(f, net, "10.90.90", true)
	d := vmOf(t, l, f, "web", "srv", virt.StateRunning)
	f.AddVM(virtfake.VM{Name: GatewayVM, State: "shut off", Ifaces: []virt.Interface{{Type: "network", Network: net, MAC: coreconf.GatewayMAC("10.90.90")}}})

	foreign := *d
	foreign.Desc = "créée à la main"
	f.AddVM(foreign)
	if err := l.ModuleDestroy("web", nil, nil); err == nil || !strings.Contains(err.Error(), "pas été créée par les scripts") {
		t.Fatalf("VM étrangère : refus attendu, obtenu %v", err)
	}
	if f.Network(net) == nil {
		t.Fatal("réseau supprimé malgré le refus")
	}

	f.AddVM(*d)
	f.Calls = nil
	var out bytes.Buffer
	if err := l.ModuleDestroy("web", nil, &out); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if f.VM(d.Name) != nil || f.Network(net) != nil {
		t.Errorf("VM ou réseau restants ; opérations %q", f.Calls)
	}
	if ifs := f.VM(GatewayVM).Ifaces; len(ifs) != 0 {
		t.Errorf("passerelle encore branchée : %+v", ifs)
	}
}

// up branche la passerelle (arrêtée) sur le réseau du module, avec la MAC dérivée du sous-réseau.
func TestUpAttachesGateway(t *testing.T) {
	l, f := engineLab(t, map[string]string{"web": webYAML})
	vmOf(t, l, f, "web", "srv", "shut off")
	f.AddVM(virtfake.VM{Name: GatewayVM, State: "shut off"})
	if err := l.ModuleUp("web", nil, UpOptions{}); err != nil {
		t.Fatal(err)
	}
	ifs := f.VM(GatewayVM).Ifaces
	if len(ifs) != 1 || ifs[0].Network != netName(t, l, "web") || ifs[0].MAC != coreconf.GatewayMAC("10.90.90") {
		t.Errorf("cartes de la passerelle : %+v", ifs)
	}
}

// plan (up --dry-run) décrit sans rien modifier.
func TestPlanChangesNothing(t *testing.T) {
	l, f := engineLab(t, map[string]string{"web": "module: web\nnetwork: {subnet: 10.90.90.0/24}\nvms:\n  srv: {ip: 10}\n  cli: {ip: 11}\n"})
	d := vmOf(t, l, f, "web", "srv", "shut off")
	d.CPUs++
	f.AddVM(*d)
	var out bytes.Buffer
	if err := l.ModulePlan("web", nil, &out); err != nil {
		t.Fatal(err)
	}
	wantCalls(t, f)
	for _, want := range []string{"à créer", "VM cli      : à créer", "VM srv      : présente, écarts — cpus"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("plan sans %q :\n%s", want, out.String())
		}
	}
}

// status compare sans modifier : écarts, snapshot de référence absent, IP prévue d'une VM non créée.
func TestModuleStatus(t *testing.T) {
	l, f := engineLab(t, map[string]string{"web": "module: web\nnetwork: {subnet: 10.90.90.0/24}\nvms:\n  srv: {ip: 10, snapshot: base}\n  cli: {ip: 11}\n"})
	isolatedNet(f, netName(t, l, "web"), "10.90.90", false)
	d := vmOf(t, l, f, "web", "srv", "shut off")
	d.MemMiB++
	f.AddVM(*d)
	st, err := l.ModuleStatus("web")
	if err != nil {
		t.Fatal(err)
	}
	wantCalls(t, f)
	if st.NetState != "inactif" || st.Gateway != virt.StateAbsent || len(st.VMs) != 2 {
		t.Fatalf("état : %+v", st)
	}
	vms := map[string]ModuleVMStatus{}
	for _, v := range st.VMs {
		vms[v.Name] = v
	}
	if s := vms["srv"]; len(s.Diffs) != 1 || s.Diffs[0].Field != "memory" || !slices.Contains(s.Problems, "snapshot base absent") {
		t.Errorf("srv : %+v", s)
	}
	if c := vms["cli"]; c.State != virt.StateAbsent || c.IP != "10.90.90.11" {
		t.Errorf("cli : %+v", c)
	}
}
