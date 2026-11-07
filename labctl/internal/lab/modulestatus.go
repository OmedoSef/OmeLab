package lab

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"labs/labctl/internal/module"
	"labs/labctl/internal/virt"
)

// Moteur des modules : état comparé à la description, et vérification de l'hôte (voir moduleup.go).

// ---------------------------------------------------------------- état

// ModuleVMStatus est l'état d'une VM de module comparé à sa description.
type ModuleVMStatus struct {
	Name      string   `json:"name"`
	Libvirt   string   `json:"libvirt"`
	Role      string   `json:"role"`
	State     string   `json:"state"`
	IP        string   `json:"ip"`
	Snapshot  string   `json:"snapshot"`  // snapshot de référence déclaré
	Snapshots []string `json:"snapshots"` // snapshots existants
	Diffs     []Diff   `json:"diffs"`
	Problems  []string `json:"problems"` // isolation, snapshot manquant...
}

// ModuleStatus est l'état d'un module déclaratif.
type ModuleStatus struct {
	Name      string           `json:"name"`
	Network   string           `json:"network"`
	Subnet    string           `json:"subnet"`
	Domain    string           `json:"domain"`
	NetState  string           `json:"netState"` // absent, inactif, actif
	Gateway   string           `json:"gateway"`  // état de core-gw
	Attached  bool             `json:"attached"` // passerelle branchée sur le réseau du module
	CA        string           `json:"ca"`       // "" si pas de CA déclarée
	CAOK      bool             `json:"caOk"`
	CoreDirty bool             `json:"coreDirty"` // configuration DHCP/DNS à appliquer
	VMs       []ModuleVMStatus `json:"vms"`
}

// ModuleStatus compare un module déclaratif à la réalité (sans rien modifier).
func (l *Lab) ModuleStatus(name string) (*ModuleStatus, error) {
	m, err := l.Module(name)
	if err != nil {
		return nil, err
	}
	if m.YAMLErr != nil {
		return nil, m.YAMLErr
	}
	if m.YAML == nil {
		return nil, fmt.Errorf("le module %s n'a pas de %s", name, module.FileName)
	}
	r := &run{l: l, y: m.YAML, out: io.Discard}
	v := l.Hypervisor()
	st := &ModuleStatus{Name: name, Network: r.y.NetworkName(), Domain: r.domain(), Gateway: l.DomState(GatewayVM), NetState: "absent"}
	if p := r.y.Subnet(); p.IsValid() {
		st.Subnet = p.String()
	}
	if v.NetExists(st.Network) {
		st.NetState = "inactif"
		if v.NetActive(st.Network) {
			st.NetState = "actif"
		}
	}
	st.Attached = st.Gateway != virt.StateAbsent && r.gatewayAttached()
	if r.y.PKI != nil {
		st.CA = r.y.PKI.CA
		_, err := os.Stat(filepath.Join(l.PKIDir(), "sub-ca", st.CA, "ca.crt"))
		st.CAOK = err == nil
	}
	if st.Gateway == virt.StateRunning {
		if ch, err := l.PendingCoreChanges(); err == nil {
			st.CoreDirty = len(ch) > 0
		}
	}
	for _, vm := range r.y.VMNames() {
		e, lv := r.eff(vm), r.libvirtName(vm)
		vs := ModuleVMStatus{Name: vm, Libvirt: lv, Role: e.Role, State: l.DomState(lv), Snapshot: e.Snapshot, Diffs: []Diff{}, Problems: []string{}}
		if vs.State != virt.StateAbsent {
			vs.IP = r.vmIP(vm)
			vs.Snapshots = l.SnapshotsOf(lv)
			diffs, err := r.diffs(vm)
			if err != nil {
				vs.Problems = append(vs.Problems, err.Error())
			}
			vs.Diffs = append(vs.Diffs, diffs...)
			if e.Snapshot != "" && !slices.Contains(vs.Snapshots, e.Snapshot) {
				vs.Problems = append(vs.Problems, "snapshot "+e.Snapshot+" absent")
			}
			for _, c := range e.VM.Certificates {
				if r.y.PKI == nil {
					continue
				}
				dns, ips := r.certWant(vm, c)
				path := filepath.Join(l.PKIDir(), "certs", c.Name, c.Name+".crt")
				if _, err := os.Stat(path); err != nil {
					vs.Problems = append(vs.Problems, "certificat "+c.Name+" pas encore émis")
				} else if why := r.certStale(path, c, dns, ips); why != "" {
					vs.Problems = append(vs.Problems, "certificat "+c.Name+" à réémettre : "+why)
				}
			}
		} else if e.IP > 0 && r.y.Prefix3() != "" {
			vs.IP = fmt.Sprintf("%s.%d", r.y.Prefix3(), e.IP)
		}
		st.VMs = append(st.VMs, vs)
	}
	return st, nil
}

// ModuleCheck vérifie que l'hôte peut faire tourner le module (KVM, libvirt, outils, clé SSH, PKI,
// passerelle, espace disque). Renvoie une erreur si un point bloque.
func (l *Lab) ModuleCheck(name string, out io.Writer) error {
	r, err := l.loadRun(name, out)
	if err != nil {
		return err
	}
	var tools []string
	for _, vm := range r.y.VMNames() {
		if r.eff(vm).Source != nil {
			tools = []string{"7z"}
		}
	}
	fail := l.HostCheck(out, tools...)
	chk := func(ok bool, what, fix string) {
		if ok {
			r.logf("ok  %s", what)
			return
		}
		r.logf("attention: %s -> %s", what, fix)
		fail++
	}
	if r.y.PKI != nil {
		chk(l.PKITool(io.Discard).Initialized(), "PKI des labs initialisée", "labctl pki init")
	}
	chk(l.DomState(GatewayVM) != virt.StateAbsent, "passerelle "+GatewayVM+" (DHCP, DNS, Internet des modules)", "labctl core gw")
	if out, err := exec.Command("df", "-BG", "--output=avail", l.VMDir()+"/..").Output(); err == nil {
		var gb int
		fmt.Sscanf(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(string(out)), "Avail")), "%dG", &gb)
		lines := strings.Fields(string(out))
		if len(lines) > 0 {
			fmt.Sscanf(lines[len(lines)-1], "%dG", &gb)
		}
		need := 0
		for _, vm := range r.y.VMNames() {
			need += r.eff(vm).Disk
		}
		chk(gb >= need, fmt.Sprintf("espace libre pour les VMs : %d Gio (jusqu'à %d Gio pour ce module)", gb, need), "libérer de la place")
	}
	if fail > 0 {
		return fmt.Errorf("%d point(s) à corriger", fail)
	}
	r.logf("ok  hôte prêt pour le module %s", name)
	return nil
}
