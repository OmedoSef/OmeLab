package lab

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"time"

	"labs/labctl/internal/portal"
	"labs/labctl/internal/virt"
	"labs/labctl/internal/vmdef"
)

// Passerelle core-gw : création, mise à jour, démarrage, arrêt, état (voir gateway.go).

// ---------------------------------------------------------------- cycle de vie de la passerelle

// Create crée la passerelle si besoin (Debian, nftables, dnsmasq, fichiers système), la démarre, la
// branche sur chaque module qui a un réseau, puis applique la configuration.
func (g *Gateway) Create() error {
	v := g.l.Hypervisor()
	if g.l.DomState(GatewayVM) != virt.StateAbsent {
		g.logf("ok  %s existe déjà", GatewayVM)
	} else {
		tmp, err := os.MkdirTemp("", "labctl-gw-")
		if err != nil {
			return err
		}
		defer os.RemoveAll(tmp)
		s := vmdef.Spec{
			Name: GatewayVM, Image: "debian13", CPUs: 1, MemoryMiB: GatewayProxyMiB, DiskGiB: 10,
			Nets: []vmdef.NetSpec{{Network: "default"}}, Lab: "core", Role: "gateway",
			Packages: []string{"nftables", "dnsmasq", "dnsmasq-utils", "conntrack"},
			Run: []string{"sysctl --system >/dev/null", "systemctl disable --now dnsmasq",
				"systemctl daemon-reload && systemctl enable --now gw.service", "systemctl enable systemd-networkd"},
		}
		for _, f := range gwInstall {
			b, err := g.l.GatewayFile(f.name)
			if err != nil {
				return err
			}
			src := filepath.Join(tmp, f.name)
			if err := os.WriteFile(src, b, 0o600); err != nil {
				return err
			}
			s.Files = append(s.Files, vmdef.FileSpec{Src: src, Dest: f.dest, Mode: f.mode})
		}
		if err := g.l.CreateVM(s, CreateOptions{Wait: true, Out: g.Out}); err != nil {
			return err
		}
	}
	if !g.Running() {
		if err := v.StartDomain(GatewayVM); err != nil {
			return err
		}
		g.waitSSH()
	}
	if err := g.installLabctl(); err != nil {
		return err
	}
	mods, err := g.gwModules()
	if err != nil {
		return err
	}
	for _, m := range mods {
		if !v.NetExists(m.Env.Net) {
			g.logf("module %s : réseau %s absent, branchement plus tard", m.Name, m.Env.Net)
			continue
		}
		if err := g.Attach(m.Name, false); err != nil {
			return err
		}
	}
	return g.Apply()
}

// waitSSH attend que la passerelle réponde en SSH (2 min au plus).
func (g *Gateway) waitSSH() bool {
	return g.l.poll(120*time.Second, 3*time.Second, func() bool { _, err := g.sshOut("true"); return err == nil })
}

// installLabctl copie labctl (binaire statique) sur la passerelle et active le service des portails captifs.
func (g *Gateway) installLabctl() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	f, err := os.Open(exe)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := g.SSH("sudo install -m 0755 /dev/stdin /usr/local/sbin/labctl", f); err != nil {
		return fmt.Errorf("copie de labctl sur la passerelle : %w", err)
	}
	// Redémarre le service s'il tourne déjà : il reprend le nouveau binaire (les sessions, persistées
	// dans /run/gw, sont relues au démarrage). Sinon on l'active simplement.
	if _, err := g.SSH("sudo systemctl daemon-reload && sudo systemctl enable -q gwportal && sudo systemctl try-restart gwportal", nil); err != nil {
		return err
	}
	g.logf("ok  labctl copié sur la passerelle (portails captifs)")
	return nil
}

// PortalSessions renvoie les sessions des portails captifs (vide si la passerelle n'en a pas).
func (g *Gateway) PortalSessions() ([]portal.Session, error) {
	out, err := g.sshOut("[ -x /usr/local/sbin/labctl ] && /usr/local/sbin/labctl gw-portal sessions --json || echo '[]'")
	if err != nil {
		return nil, err
	}
	var list []portal.Session
	return list, json.Unmarshal(out, &list)
}

// PortalKick ferme la session de ip sur le portail id.
func (g *Gateway) PortalKick(id int, ip string) error {
	// IPv4 sans zone : ParseAddr accepte une zone IPv6 arbitraire (fe80::1%$(id)), qui partirait dans le shell
	a, err := netip.ParseAddr(ip)
	if err != nil || !a.Is4() || a.Zone() != "" {
		return fmt.Errorf("adresse invalide : %q", ip)
	}
	_, err = g.SSH(fmt.Sprintf("/usr/local/sbin/labctl gw-portal kick %d %s", id, a), nil)
	return err
}

// Update pousse la version courante des fichiers système (embarqués ou remplacés), puis applique.
func (g *Gateway) Update() error {
	if err := g.require(); err != nil {
		return err
	}
	if _, err := g.SSH("dpkg -s dnsmasq dnsmasq-utils conntrack >/dev/null 2>&1 || sudo DEBIAN_FRONTEND=noninteractive apt-get install -qq -y dnsmasq dnsmasq-utils conntrack >/dev/null", nil); err != nil {
		return err
	}
	for _, f := range gwInstall {
		b, err := g.l.GatewayFile(f.name)
		if err != nil {
			return err
		}
		if _, err := g.SSH(fmt.Sprintf("sudo mkdir -p %s && sudo install -m %s /dev/stdin %s", filepath.Dir(f.dest), f.mode, f.dest), bytes.NewReader(b)); err != nil {
			return fmt.Errorf("%s : %w", f.dest, err)
		}
	}
	// Le service dnsmasq de Debian n'est pas utilisé : instances gwdns@ (dhcp, <module>-open/closed)
	if _, err := g.SSH("sudo systemctl disable -q --now dnsmasq; sudo systemctl daemon-reload && sudo /usr/sbin/sysctl -q --system", nil); err != nil {
		return err
	}
	if err := g.installLabctl(); err != nil {
		return err
	}
	g.logf("ok  fichiers système de la passerelle à jour")
	if err := g.gatewayMemory(); err != nil {
		return err
	}
	return g.Apply()
}

// Start démarre la passerelle (Internet fermé pour tous les modules).
func (g *Gateway) Start() error {
	switch g.l.DomState(GatewayVM) {
	case virt.StateAbsent:
		return errors.New("passerelle absente : labctl core gw")
	case virt.StateRunning:
	default:
		if err := g.l.Hypervisor().StartDomain(GatewayVM); err != nil {
			return err
		}
	}
	g.logf("ok  %s démarrée (Internet fermé pour tous les modules)", GatewayVM)
	return nil
}

// Stop arrête la passerelle (ACPI).
func (g *Gateway) Stop() error {
	if !g.Running() {
		g.logf("ok  %s déjà arrêtée", GatewayVM)
		return nil
	}
	if err := g.l.Hypervisor().ShutdownDomain(GatewayVM); err != nil {
		return err
	}
	g.logf("ok  %s en cours d'arrêt : les modules n'ont plus de DHCP, DNS ni Internet", GatewayVM)
	return nil
}

// Destroy supprime la passerelle.
func (g *Gateway) Destroy() error {
	return g.l.DestroyVM(GatewayVM, false, g.Out)
}

// PrintStatus affiche la passerelle, ses cartes et services, l'état Internet et les liens.
func (g *Gateway) PrintStatus() error {
	st := g.l.Core()
	if st.State == virt.StateAbsent {
		g.logf("attention: passerelle absente (labctl core gw)")
		return nil
	}
	wan := st.WAN
	if wan == "" {
		wan = "?"
	}
	g.logf("==> Passerelle %s : %s, WAN %s", GatewayVM, st.State, wan)
	if ifs, err := g.l.Hypervisor().Interfaces(GatewayVM); err == nil {
		for _, i := range ifs {
			g.logf("  carte %-18s sur %s", i.MAC, i.Network)
		}
	}
	if st.Error != "" {
		g.logf("attention: %s", st.Error)
	} else if st.State == virt.StateRunning {
		if st.LabctlStale {
			g.logf("attention: %s -> labctl core update", st.LabctlNote())
		} else {
			g.logf("  labctl %s (même version que l'hôte)", st.Labctl)
		}
	}
	for _, s := range st.Services {
		if strings.HasPrefix(s.Name, "gwdns@") {
			g.logf("  service %-28s %s", s.Name+".service", s.Sub)
		}
	}
	fmt.Fprintln(g.Out)
	if err := g.PrintInternet(); err != nil {
		return err
	}
	fmt.Fprintln(g.Out)
	return g.PrintLinks()
}
