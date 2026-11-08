package lab

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"labs/labctl/internal/coreconf"
	"labs/labctl/internal/fetch"
	"labs/labctl/internal/virt"
)

// Proxy transparent de la passerelle (connexions via: proxy) : mitmproxy, version figée, installé dans la
// passerelle à la première connexion qui en a besoin. L'archive officielle est vérifiée par son empreinte
// SHA-256 ; celle-ci figure dans l'attestation de build signée de mitmproxy (provenance SLSA de la release
// v12.3.1), dont l'empreinte du paquet Python concorde avec celle publiée par PyPI.
const (
	mitmVersion = "12.3.1"
	mitmSHA256  = "f7b4237f5702f6cd21357fe6e4680a7e905f8f9b816b872230d018ea696c34d5"
	// GatewayProxyMiB : mémoire de la passerelle quand le proxy sert (mitmweb garde les échanges en mémoire)
	GatewayProxyMiB = 1024
)

func mitmURL() string {
	return fmt.Sprintf("https://downloads.mitmproxy.org/%s/mitmproxy-%s-linux-x86_64.tar.gz", mitmVersion, mitmVersion)
}

// mitmTarball renvoie l'archive de mitmproxy du cache (téléchargée et vérifiée si besoin).
func (l *Lab) mitmTarball(g *Gateway) (string, error) {
	path := filepath.Join(l.ImagesDir(), "sources", "cache", fmt.Sprintf("mitmproxy-%s-linux-x86_64.tar.gz", mitmVersion))
	return path, fetch.Cached(mitmURL(), path, "sha256", mitmSHA256, fmt.Sprintf("mitmproxy %s (~105 Mo)", mitmVersion), g.Out)
}

// gatewayMemory porte la mémoire de la passerelle à GatewayProxyMiB (redémarrage : les modules perdent DHCP,
// DNS et Internet une trentaine de secondes).
func (g *Gateway) gatewayMemory() error {
	v := g.l.Hypervisor()
	cpus, mem, err := v.Resources(GatewayVM)
	if err != nil || mem >= GatewayProxyMiB {
		return err
	}
	g.logf("==> mémoire de la passerelle : %d -> %d Mio (redémarrage)", mem, GatewayProxyMiB)
	if err := v.SetResources(GatewayVM, cpus, GatewayProxyMiB); err != nil {
		return err
	}
	if !g.Running() {
		return nil
	}
	if err := v.ShutdownDomain(GatewayVM); err != nil {
		return err
	}
	if !g.l.poll(90*time.Second, time.Second, func() bool { return g.l.DomState(GatewayVM) != virt.StateRunning }) {
		return fmt.Errorf("la passerelle ne s'arrête pas : labctl core stop puis start")
	}
	if err := v.StartDomain(GatewayVM); err != nil {
		return err
	}
	if !g.waitSSH() {
		return fmt.Errorf("la passerelle ne répond plus en SSH après le redémarrage")
	}
	return nil
}

// ensureProxy installe et lance le proxy si une connexion via: proxy existe, sinon l'arrête.
func (g *Gateway) ensureProxy(files coreconf.Files) error {
	var px []coreconf.ProxyConf
	_ = json.Unmarshal([]byte(files["gwdns/proxy.json"]), &px)
	if len(px) == 0 {
		_, err := g.SSH("if systemctl is-enabled -q gwproxy 2>/dev/null; then sudo systemctl disable -q --now gwproxy; fi", nil)
		return err
	}
	if _, mem, err := g.l.Hypervisor().Resources(GatewayVM); err == nil && mem < GatewayProxyMiB {
		g.logf("attention: passerelle à %d Mio : %d conseillés pour le proxy (labctl core update)", mem, GatewayProxyMiB)
	}
	if out, _ := g.sshOut("cat /opt/mitmproxy/VERSION 2>/dev/null"); strings.TrimSpace(string(out)) != mitmVersion {
		tar, err := g.l.mitmTarball(g)
		if err != nil {
			return err
		}
		f, err := os.Open(tar)
		if err != nil {
			return err
		}
		defer f.Close()
		g.logf("==> installation de mitmproxy %s dans la passerelle", mitmVersion)
		if _, err := g.SSH(fmt.Sprintf(`set -e; sudo rm -rf /opt/mitmproxy.new; sudo mkdir -p /opt/mitmproxy.new
sudo tar -xzf - -C /opt/mitmproxy.new mitmweb mitmdump
echo %s | sudo tee /opt/mitmproxy.new/VERSION >/dev/null
sudo rm -rf /opt/mitmproxy; sudo mv /opt/mitmproxy.new /opt/mitmproxy`, mitmVersion), f); err != nil {
			return fmt.Errorf("installation de mitmproxy : %w", err)
		}
	}
	// Fichiers du service (aussi poussés par core update), autorité d'interception, mot de passe de mitmweb
	for _, f := range gwInstall {
		if !strings.HasPrefix(f.name, "gwproxy") {
			continue
		}
		b, err := g.l.GatewayFile(f.name)
		if err != nil {
			return err
		}
		if _, err := g.SSH(fmt.Sprintf("sudo mkdir -p %s && sudo install -m %s /dev/stdin %s", filepath.Dir(f.dest), f.mode, f.dest), bytes.NewReader(b)); err != nil {
			return err
		}
	}
	cert, bundle, err := g.l.PKITool(g.Out).InterceptionCA()
	if err != nil {
		return fmt.Errorf("autorité d'interception : %w", err)
	}
	if _, err := g.SSH("sudo install -d -m 0700 /etc/gwproxy && sudo install -m 0600 /dev/stdin /etc/gwproxy/mitmproxy-ca.pem", bytes.NewReader(bundle)); err != nil {
		return err
	}
	if _, err := g.SSH("sudo install -m 0644 /dev/stdin /etc/gwproxy/mitmproxy-ca-cert.pem", bytes.NewReader(cert)); err != nil {
		return err
	}
	if _, err := g.SSH(`sudo test -f /etc/gwproxy/env || echo "WEB_PASSWORD=$(head -c 18 /dev/urandom | base64 | tr -d '/+=')" | sudo install -m 0600 /dev/stdin /etc/gwproxy/env
sudo systemctl daemon-reload && sudo systemctl enable -q gwproxy && sudo systemctl restart gwproxy`, nil); err != nil {
		return err
	}
	g.logf("ok  proxy de la passerelle : %d connexion(s) via: proxy", len(px))
	return nil
}

// ProxyEvent : une ligne du journal du proxy (/var/log/gwproxy/flows.jsonl sur la passerelle).
type ProxyEvent struct {
	Time   string `json:"time"`
	Kind   string `json:"kind"` // tls | http
	Src    string `json:"src"`
	Dst    string `json:"dst"`
	Host   string `json:"host"`
	Method string `json:"method,omitempty"`
	URL    string `json:"url,omitempty"`
	Module string `json:"module,omitempty"`
	Target string `json:"target,omitempty"`
	Mode   string `json:"mode,omitempty"`
	Action string `json:"action"` // passé | refusé | intercepté
	Why    string `json:"why,omitempty"`
	Status int    `json:"status,omitempty"`
	Size   int    `json:"size,omitempty"`
	Error  string `json:"error,omitempty"`
}

// ProxyLog renvoie les n derniers événements du proxy (les plus récents d'abord).
func (g *Gateway) ProxyLog(n int) ([]ProxyEvent, error) {
	if err := g.require(); err != nil {
		return nil, err
	}
	out, err := g.sshOut(fmt.Sprintf("sudo tail -n %d /var/log/gwproxy/flows.jsonl 2>/dev/null || true", n))
	if err != nil {
		return nil, err
	}
	var ev []ProxyEvent
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		var e ProxyEvent
		if json.Unmarshal([]byte(lines[i]), &e) == nil {
			ev = append(ev, e)
		}
	}
	return ev, nil
}

// ProxyWebPassword renvoie le mot de passe de mitmweb ("" si le proxy n'est pas installé).
func (g *Gateway) ProxyWebPassword() string {
	out, err := g.sshOut("sudo sed -n 's/^WEB_PASSWORD=//p' /etc/gwproxy/env 2>/dev/null")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// ProxyTunnel ouvre un tunnel SSH vers mitmweb (127.0.0.1:8081 de la passerelle) sur un port libre de l'hôte
// (127.0.0.1 seulement) ; renvoie l'adresse locale et le processus ssh (à arrêter par l'appelant).
func (g *Gateway) ProxyTunnel() (addr string, cmd *exec.Cmd, err error) {
	t, err := g.target()
	if err != nil {
		return "", nil, err
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", nil, err
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	args := append(g.l.SSHArgs(true), "-o", "ExitOnForwardFailure=yes", "-N",
		"-L", fmt.Sprintf("127.0.0.1:%d:127.0.0.1:%d", port, coreconf.ProxyWebPort), t)
	cmd = exec.Command("ssh", args...)
	if err := cmd.Start(); err != nil {
		return "", nil, err
	}
	addr = fmt.Sprintf("127.0.0.1:%d", port)
	for i := 0; i < 50; i++ { // tunnel prêt quand le port local répond
		if c, err := net.DialTimeout("tcp", addr, 200*time.Millisecond); err == nil {
			c.Close()
			return addr, cmd, nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	_ = cmd.Process.Kill()
	return "", nil, fmt.Errorf("tunnel SSH vers mitmweb non établi (proxy installé ? labctl core status)")
}
