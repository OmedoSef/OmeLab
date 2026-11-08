package gui

import (
	"fmt"
	"net/http"
	"net/url"

	"labs/labctl/internal/lab"
)

// Proxy de la passerelle : journal du trafic et tunnel vers mitmweb.

// --- Proxy de la passerelle : journal, interface mitmweb (tunnel SSH)

func (s *server) handleProxyLog(w http.ResponseWriter, r *http.Request) {
	n := 300
	fmt.Sscan(r.URL.Query().Get("n"), &n)
	if n < 1 || n > 5000 {
		n = 300
	}
	ev, err := s.lab.Gateway(nil).ProxyLog(n)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	if ev == nil {
		ev = []lab.ProxyEvent{}
	}
	writeJSON(w, ev)
}

// handleProxyWeb ouvre (ou réutilise) le tunnel vers mitmweb et renvoie son adresse et son mot de passe.
func (s *server) handleProxyWeb(w http.ResponseWriter, r *http.Request) {
	s.tunnel.Lock()
	defer s.tunnel.Unlock()
	g := s.lab.Gateway(nil)
	alive := s.tunDone != nil
	if alive {
		select {
		case <-s.tunDone:
			alive = false
		default:
		}
	}
	if !alive {
		addr, cmd, err := g.ProxyTunnel()
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		done := make(chan struct{})
		s.tunAddr, s.tunCmd, s.tunDone = addr, cmd, done
		go func() { _ = cmd.Wait(); close(done) }()
	}
	pw := g.ProxyWebPassword()
	writeJSON(w, map[string]string{"url": "http://" + s.tunAddr + "/?token=" + url.QueryEscape(pw), "password": pw})
}
