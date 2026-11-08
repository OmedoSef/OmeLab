package gui

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"

	"labs/labctl/internal/lab"
	"labs/labctl/internal/module"
)

// Éditeur de module.yaml : lecture, squelette, validation à la volée, enregistrement.

// ---------------------------------------------------------------- éditeur de module.yaml

// issue est un problème de validation tel que l'affiche l'éditeur.
type issue struct {
	Error bool   `json:"error"`
	Path  string `json:"path"`
	Line  int    `json:"line"`
	Msg   string `json:"msg"`
}

func issuesJSON(in []module.Issue) []issue {
	out := []issue{}
	for _, i := range in {
		out = append(out, issue{i.Error, i.Path, i.Line, i.Msg})
	}
	return out
}

// handleModules liste les modules déclaratifs (module.yaml).
func (s *server) handleModules(w http.ResponseWriter, _ *http.Request) {
	mods, err := s.lab.Modules()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	names := []string{}
	for _, m := range mods {
		if m.HasYAML {
			names = append(names, m.Name)
		}
	}
	writeJSON(w, map[string]any{"modules": names, "reserved": module.ReservedNames})
}

func (s *server) handleModuleGet(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("name")
	text, hash, err := s.lab.ModuleText(name)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	writeJSON(w, map[string]any{"name": name, "text": text, "hash": hash, "issues": issuesJSON(s.lab.CheckModuleText(name, text))})
}

func (s *server) handleModuleSkeleton(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("name")
	text := lab.ModuleSkeleton(name)
	writeJSON(w, map[string]any{"name": name, "text": text, "issues": issuesJSON(s.lab.CheckModuleText(name, text))})
}

type moduleText struct {
	Name   string `json:"name"`
	Text   string `json:"text"`
	Hash   string `json:"hash"`   // empreinte du fichier lu ("" pour un nouveau module)
	Create bool   `json:"create"` // nouveau module
}

func decodeModuleText(w http.ResponseWriter, r *http.Request) (moduleText, bool) {
	var req moduleText
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil || req.Name == "" {
		http.Error(w, "requête invalide", http.StatusBadRequest)
		return req, false
	}
	return req, true
}

// handleModuleCheck valide un texte sans l'enregistrer (validation à la volée).
func (s *server) handleModuleCheck(w http.ResponseWriter, r *http.Request) {
	req, ok := decodeModuleText(w, r)
	if !ok {
		return
	}
	writeJSON(w, map[string]any{"issues": issuesJSON(s.lab.CheckModuleText(req.Name, req.Text))})
}

// handleModuleSave enregistre un texte valide (refusé s'il contient des erreurs ou si le fichier a changé).
func (s *server) handleModuleSave(w http.ResponseWriter, r *http.Request) {
	req, ok := decodeModuleText(w, r)
	if !ok {
		return
	}
	if !s.busy.TryLock() {
		http.Error(w, "une action est en cours", http.StatusConflict)
		return
	}
	defer s.busy.Unlock()
	hash, issues, err := s.lab.SaveModuleText(req.Name, req.Text, req.Hash, req.Create)
	resp := map[string]any{"hash": hash, "issues": issuesJSON(issues)}
	if err != nil {
		code := http.StatusBadRequest
		if errors.Is(err, lab.ErrConflict) {
			code = http.StatusConflict
		}
		resp["error"] = err.Error()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(resp)
		return
	}
	log.Printf("module.yaml enregistré : %s", req.Name)
	s.invalidate()
	writeJSON(w, resp)
}
