package gui

import (
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"time"
)

// Lecture de l'état : lab, passerelle, PKI.

func (s *server) handleState(w http.ResponseWriter, r *http.Request) {
	// L'état interroge libvirt et la passerelle (SSH) : on le garde 2 s pour ne pas surcharger
	s.cache.Lock()
	defer s.cache.Unlock()
	if s.state == nil || time.Since(s.stated) > 2*time.Second || r.URL.Query().Has("fresh") {
		st, err := s.lab.Status()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		s.state, s.stated = st, time.Now()
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(s.state)
}

func (s *server) handleCore(w http.ResponseWriter, r *http.Request) {
	// Une connexion SSH à la passerelle : gardée 3 s
	s.cache.Lock()
	if s.core == nil || time.Since(s.cored) > 3*time.Second || r.URL.Query().Has("fresh") {
		s.core, s.cored = s.lab.Core(), time.Now()
	}
	cs := s.core
	s.cache.Unlock()
	writeJSON(w, cs)
}

func (s *server) handlePKI(w http.ResponseWriter, r *http.Request) {
	st, err := s.lab.PKI()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"pki": st, "vms": s.lab.AllVMNames()})
}

// handlePKITrust sonde en SSH quelles VMs font confiance à la racine / à l'autorité d'interception. Coûteux
// (un aller-retour par VM) : déclenché par le bouton « Vérifier la confiance », jamais par le rafraîchissement.
func (s *server) handlePKITrust(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.lab.TrustStatus(s.lab.AllVMNames()...))
}

// handlePKIFile sert un certificat public de la PKI (jamais une clé privée ni un .p12).
func (s *server) handlePKIFile(w http.ResponseWriter, r *http.Request) {
	p, err := s.lab.PublicFile(r.URL.Query().Get("path"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	w.Header().Set("Content-Type", "application/x-pem-file")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filepath.Base(p)))
	http.ServeFile(w, r, p)
}
