package gui

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"

	"labs/labctl/internal/lab"
)

// Fichiers d'un module (files/) : liste, lecture, enregistrement, envoi, renommage, suppression.

// --- Fichiers complémentaires d'un module (modules/<m>/files/)

func (s *server) handleFiles(w http.ResponseWriter, r *http.Request) {
	fl, err := s.lab.ModuleFiles(r.URL.Query().Get("module"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, fl)
}

func (s *server) handleFileText(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	text, hash, err := s.lab.ModuleFileText(q.Get("module"), q.Get("path"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, map[string]string{"text": text, "hash": hash})
}

// handleFileRaw renvoie le fichier en pièce jointe (jamais affiché par le navigateur).
func (s *server) handleFileRaw(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	p, err := s.lab.ModuleFilePath(q.Get("module"), q.Get("path"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filepath.Base(p)))
	http.ServeFile(w, r, p)
}

type fileSave struct {
	Module string `json:"module"`
	Path   string `json:"path"`
	Text   string `json:"text"`
	Hash   string `json:"hash"`
	Create bool   `json:"create"`
}

func (s *server) handleFileSave(w http.ResponseWriter, r *http.Request) {
	var req fileSave
	if err := json.NewDecoder(io.LimitReader(r.Body, lab.MaxTextFile*2)).Decode(&req); err != nil {
		http.Error(w, "requête invalide", http.StatusBadRequest)
		return
	}
	hash, err := s.lab.SaveModuleFileText(req.Module, req.Path, req.Text, req.Hash, req.Create)
	if err != nil {
		code := http.StatusBadRequest
		if errors.Is(err, lab.ErrConflict) || errors.Is(err, lab.ErrExists) {
			code = http.StatusConflict
		}
		http.Error(w, err.Error(), code)
		return
	}
	writeJSON(w, map[string]string{"hash": hash})
}

// handleFileUpload : corps brut = contenu du fichier ; module, path et overwrite dans l'adresse.
func (s *server) handleFileUpload(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	body := http.MaxBytesReader(w, r.Body, lab.MaxUpload+1)
	err := s.lab.UploadModuleFile(q.Get("module"), q.Get("path"), body, q.Get("overwrite") == "1")
	if err != nil {
		code := http.StatusBadRequest
		if errors.Is(err, lab.ErrExists) {
			code = http.StatusConflict
		}
		http.Error(w, err.Error(), code)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

type fileOp struct {
	Module string `json:"module"`
	Op     string `json:"op"` // mkdir, rename, delete
	Path   string `json:"path"`
	To     string `json:"to"`
}

func (s *server) handleFileOp(w http.ResponseWriter, r *http.Request) {
	var req fileOp
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<12)).Decode(&req); err != nil {
		http.Error(w, "requête invalide", http.StatusBadRequest)
		return
	}
	var err error
	switch req.Op {
	case "mkdir":
		err = s.lab.MkdirModuleFile(req.Module, req.Path)
	case "rename":
		err = s.lab.RenameModuleFile(req.Module, req.Path, req.To)
	case "delete":
		err = s.lab.DeleteModuleFile(req.Module, req.Path)
	default:
		err = fmt.Errorf("opération inconnue : %q", req.Op)
	}
	if err != nil {
		code := http.StatusBadRequest
		if errors.Is(err, lab.ErrExists) {
			code = http.StatusConflict
		}
		http.Error(w, err.Error(), code)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}
