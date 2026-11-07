package lab

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net/netip"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"labs/labctl/internal/portal"
	"labs/labctl/internal/virt"
)

// CoreState détaille la passerelle core-gw : cartes, services, baux DHCP, pare-feu.
type CoreState struct {
	State    string           `json:"state"`
	WAN      string           `json:"wan"`
	Networks []string         `json:"networks"` // réseaux libvirt sur lesquels elle est branchée
	Services []ServiceInfo    `json:"services"`
	Leases   []LeaseInfo      `json:"leases"`
	Allowed  []string         `json:"allowed"` // sources autorisées à sortir (gwctl)
	Blocked  []string         `json:"blocked"` // sources bloquées (VMs internet: never + manuelles)
	Portal   []portal.Session `json:"portal"`  // sessions des portails captifs
	Error    string           `json:"error,omitempty"`
	// Copie de labctl sur la passerelle (service des portails captifs) : sa version, et si elle diffère de celle
	// de l'hôte (absente ou autre version) — à corriger par « labctl core update » (fait aussi à chaque démarrage).
	Labctl      string `json:"labctl,omitempty"`
	HostLabctl  string `json:"hostLabctl,omitempty"`
	LabctlStale bool   `json:"labctlStale,omitempty"`
}

// Version : version du labctl qui tourne sur l'hôte (fixée par la CLI au démarrage), comparée à la copie de la
// passerelle.
var Version = "dev"

// LabctlNote décrit l'écart entre la copie de labctl de la passerelle et celle de l'hôte (si LabctlStale).
func (cs *CoreState) LabctlNote() string {
	if cs.Labctl == "" {
		return "labctl absent de la passerelle (portails captifs indisponibles)"
	}
	return fmt.Sprintf("labctl de la passerelle en version %s, hôte en %s", cs.Labctl, cs.HostLabctl)
}

type ServiceInfo struct {
	Name   string `json:"name"`
	Active string `json:"active"` // active, inactive, failed...
	Sub    string `json:"sub"`    // running, exited, dead...
	Role   string `json:"role"`
}

type LeaseInfo struct {
	Expires string `json:"expires"`
	MAC     string `json:"mac"`
	IP      string `json:"ip"`
	Name    string `json:"name"`
	Module  string `json:"module"`
}

// Script exécuté sur la passerelle : une section par type d'information
const coreProbe = `echo '@services'; systemctl list-units --all --plain --no-legend 'gwdns@*' gw.service gwportal.service 2>/dev/null
echo '@leases'; cat /var/lib/misc/gwdhcp.leases 2>/dev/null
echo '@gwctl'; sudo gwctl status 2>/dev/null; sudo cat /etc/gwdns/blocked 2>/dev/null | sed 's/^/blocked /'
echo '@portal'; [ -x /usr/local/sbin/labctl ] && /usr/local/sbin/labctl gw-portal sessions --json 2>/dev/null; echo
echo '@labctl'; [ -x /usr/local/sbin/labctl ] && /usr/local/sbin/labctl --version 2>/dev/null; true`

// Core lit l'état détaillé de la passerelle.
func (l *Lab) Core() *CoreState {
	cs := &CoreState{State: l.DomState(GatewayVM)}
	if cs.State == virt.StateAbsent {
		return cs
	}
	if ifs, err := l.Hypervisor().Interfaces(GatewayVM); err == nil {
		for _, i := range ifs {
			if i.Type == "network" {
				cs.Networks = append(cs.Networks, i.Network)
			}
		}
	}
	cs.WAN = l.Hypervisor().IPv4(GatewayVM)
	if cs.State != virt.StateRunning {
		return cs
	}

	b, err := l.Gateway(nil).sshOut(coreProbe)
	if err != nil {
		cs.Error = "passerelle injoignable en SSH : " + err.Error()
		return cs
	}
	subnets := l.moduleSubnets()
	section := ""
	sc := bufio.NewScanner(strings.NewReader(string(b)))
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "@") {
			section = line[1:]
			continue
		}
		f := strings.Fields(line)
		switch {
		case section == "services" && len(f) >= 4:
			cs.Services = append(cs.Services, ServiceInfo{Name: strings.TrimSuffix(f[0], ".service"), Active: f[2], Sub: f[3], Role: serviceRole(f[0])})
		case section == "leases" && len(f) >= 4:
			exp := f[0]
			if sec, err := strconv.ParseInt(f[0], 10, 64); err == nil {
				exp = time.Unix(sec, 0).Format("2006-01-02 15:04")
			}
			name := f[3]
			if name == "*" {
				name = ""
			}
			cs.Leases = append(cs.Leases, LeaseInfo{Expires: exp, MAC: f[1], IP: f[2], Name: name, Module: subnets[subnetOf(f[2])]})
		case section == "gwctl" && len(f) == 2 && f[0] == "allowed":
			cs.Allowed = appendUnique(cs.Allowed, f[1])
		case section == "gwctl" && len(f) == 2 && f[0] == "blocked":
			cs.Blocked = appendUnique(cs.Blocked, f[1])
		case section == "portal" && strings.HasPrefix(line, "["):
			_ = json.Unmarshal([]byte(line), &cs.Portal)
		case section == "labctl" && len(f) == 3 && f[0] == "labctl" && f[1] == "version": // « labctl version X »
			cs.Labctl = f[2]
		}
	}
	cs.HostLabctl = Version
	cs.LabctlStale = cs.Labctl != Version // copie absente (cs.Labctl vide) ou d'une autre version
	sort.Slice(cs.Services, func(i, j int) bool { return cs.Services[i].Name < cs.Services[j].Name })
	sort.Slice(cs.Leases, func(i, j int) bool { return ipLess(cs.Leases[i].IP, cs.Leases[j].IP) })
	return cs
}

func serviceRole(unit string) string {
	u := strings.TrimSuffix(unit, ".service")
	switch {
	case u == "gw":
		return "pare-feu (appliqué au démarrage)"
	case u == "gwportal":
		return "portails captifs (connections: … via: portal)"
	case u == "gwdns@dhcp":
		return "DHCP de tous les modules"
	case strings.HasSuffix(u, "-open"):
		return "DNS " + strings.TrimSuffix(strings.TrimPrefix(u, "gwdns@"), "-open") + " : VMs autorisées (relais Internet)"
	case strings.HasSuffix(u, "-closed"):
		return "DNS " + strings.TrimSuffix(strings.TrimPrefix(u, "gwdns@"), "-closed") + " : noms du lab seulement"
	}
	return ""
}

// moduleSubnets associe le préfixe /24 de chaque module à son nom.
func (l *Lab) moduleSubnets() map[string]string {
	out := map[string]string{}
	mods, _ := l.Modules()
	for _, m := range mods {
		if m.Env != nil {
			out[m.Env.Subnet] = m.Name
		}
	}
	return out
}

func subnetOf(ip string) string {
	if i := strings.LastIndex(ip, "."); i > 0 {
		return ip[:i]
	}
	return ip
}

func appendUnique(list []string, s string) []string {
	if slices.Contains(list, s) {
		return list
	}
	return append(list, s)
}

// ipLess compare deux IPv4 numériquement (10.66.66.20 avant 10.66.66.186).
func ipLess(a, b string) bool {
	x, errA := netip.ParseAddr(a)
	y, errB := netip.ParseAddr(b)
	if errA != nil || errB != nil {
		return a < b
	}
	return x.Less(y)
}
