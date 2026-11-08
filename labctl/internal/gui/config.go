package gui

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"

	"labs/labctl/internal/lab"
	"labs/labctl/internal/module"
	"labs/labctl/internal/virt"
)

// Page réseau d'un module : réservations, alias, liens, connexions (écrits dans le module.yaml ou le câblage local).

// Configuration éditable (section dns du module.yaml) de chaque module, et changements en attente sur la passerelle.
type configHost struct {
	Name   string `json:"name"`
	IP     string `json:"ip"`
	MAC    string `json:"mac"`
	Source string `json:"source"` // vm (vms: du module, lecture seule ici) ou reservation (dns:)
}

type configModule struct {
	Name       string              `json:"name"`
	Subnet     string              `json:"subnet"`
	Domain     string              `json:"domain"`
	Hosts      []configHost        `json:"hosts"`
	Aliases    map[string]string   `json:"aliases"`
	Links      []string            `json:"links"`
	Conns      []module.Connection `json:"connections"` // connections: du module.yaml, dans l'ordre du fichier
	Vulnerable bool                `json:"vulnerable"`  // vulnerable: true : pas de connexion sortante
}

func (s *server) handleConfig(w http.ResponseWriter, r *http.Request) {
	mods, err := s.lab.Modules()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	var out []configModule
	for _, m := range mods {
		if m.Env == nil {
			continue
		}
		cm := configModule{Name: m.Name, Subnet: m.Env.Subnet, Domain: m.Env.Domain, Aliases: m.Conf.Aliases, Links: m.Conf.Links,
			Conns: []module.Connection{}, Vulnerable: m.Vulnerable()}
		if m.YAML != nil && m.YAML.Connections != nil {
			cm.Conns = m.YAML.Connections
		}
		for _, vm := range m.Env.VMs() {
			cm.Hosts = append(cm.Hosts, configHost{vm, m.Env.IP(vm), m.Env.Macs[vm], "vm"})
		}
		for h, o := range m.Conf.Hosts {
			src := "reservation"
			if m.Conf.Attached[h] != "" {
				src = "attach:" + m.Conf.Attached[h] // VM d'un autre module branchée ici (attach: du module.yaml)
			}
			cm.Hosts = append(cm.Hosts, configHost{h, fmt.Sprintf("%s.%d", m.Env.Subnet, o), m.Conf.HostMACs[h], src})
		}
		out = append(out, cm)
	}
	resp := map[string]any{"modules": out, "gateway": s.lab.DomState(lab.GatewayVM)}
	if resp["gateway"] == virt.StateRunning {
		if changes, err := s.lab.PendingCoreChanges(); err != nil {
			resp["pendingError"] = err.Error()
		} else {
			resp["pending"] = changes
		}
	}
	writeJSON(w, resp)
}

type configEdit struct {
	Op     string         `json:"op"` // host-add, host-del, alias-add, alias-del, link, unlink, conn-add, conn-del
	Module string         `json:"module"`
	Name   string         `json:"name"`
	Octet  int            `json:"octet"`
	MAC    string         `json:"mac"`
	Target string         `json:"target"`
	From   []string       `json:"from"`   // conn-add
	Ports  []string       `json:"ports"`  // conn-add
	Via    string         `json:"via"`    // conn-add : direct | portal
	Portal *module.Portal `json:"portal"` // conn-add, via portal
	Proxy  *module.Proxy  `json:"proxy"`  // conn-add, via proxy
	Local  bool           `json:"local"`  // conn-add : écrire dans network.local.yaml (propre au poste), pas le module.yaml
	Index  int            `json:"index"`  // conn-del
}

// handleConfigEdit modifie la section dns du module.yaml (sans appliquer : voir « Appliquer » dans l'onglet Réseau).
func (s *server) handleConfigEdit(w http.ResponseWriter, r *http.Request) {
	var e configEdit
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<12)).Decode(&e); err != nil {
		http.Error(w, "requête invalide", http.StatusBadRequest)
		return
	}
	if !s.busy.TryLock() {
		http.Error(w, "une action est en cours", http.StatusConflict)
		return
	}
	defer s.busy.Unlock()
	var err error
	switch e.Op {
	case "host-add":
		err = s.lab.ConfAddHost(e.Module, e.Name, e.Octet, e.MAC)
	case "host-del":
		err = s.lab.ConfDelHost(e.Module, e.Name)
	case "conn-add":
		cn := module.Connection{To: e.Target, From: e.From, Ports: e.Ports, Via: e.Via, Portal: e.Portal, Proxy: e.Proxy}
		if e.Local {
			err = s.lab.AddLocalConnection(e.Module, cn)
		} else {
			err = s.lab.ConfAddConnection(e.Module, cn)
		}
	case "conn-del":
		err = s.lab.DelConnectionAt(e.Module, e.Index)
	case "alias-add":
		err = s.lab.ConfAddAlias(e.Module, e.Name, e.Target)
	case "alias-del":
		err = s.lab.ConfDelAlias(e.Module, e.Name)
	case "link", "unlink":
		err = s.lab.ConfLink(e.Module, e.Target, e.Op == "link")
	case "attach-add":
		err = s.lab.AttachVM(e.Module, e.Target, e.Octet, io.Discard)
	case "attach-del":
		err = s.lab.DetachVM(e.Module, e.Target, io.Discard)
	default:
		err = fmt.Errorf("opération inconnue : %s", e.Op)
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	log.Printf("configuration : %s %s %s %s", e.Op, e.Module, e.Name, e.Target)
	s.invalidate()
	w.WriteHeader(http.StatusNoContent)
}
