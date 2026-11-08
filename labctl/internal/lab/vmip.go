package lab

import (
	"fmt"
	"strings"
	"time"

	"labs/labctl/internal/coreconf"
	"labs/labctl/internal/virt"
)

// Options SSH : voir lab/ssh.go (SSHArgs), seul endroit où elles sont construites.

// VMIP renvoie la première IPv4 d'une VM : bail DHCP libvirt, agent invité, ARP, puis baux de la passerelle
// core (réseaux des modules, dont le DHCP est sur core-gw). "" si aucune.
func (l *Lab) VMIP(name string) string {
	v := l.Hypervisor()
	if ip := v.IPv4(name); ip != "" && (name == GatewayVM || !strings.HasSuffix(ip, fmt.Sprintf(".%d", coreconf.GatewayOctet))) {
		return ip // sur les réseaux des modules, .254 est la passerelle : jamais l'adresse d'une autre VM
	}
	if name == GatewayVM || v.State(GatewayVM) != virt.StateRunning {
		return ""
	}
	ifs, err := v.Interfaces(name)
	if err != nil || len(ifs) == 0 {
		return ""
	}
	gw := v.IPv4(GatewayVM)
	if gw == "" {
		return ""
	}
	out, _, err := l.shell(l.VMUser()+"@"+gw, "cat /var/lib/misc/gwdhcp.leases", nil)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) < 3 {
			continue
		}
		for _, i := range ifs {
			if strings.EqualFold(f[1], i.MAC) {
				return f[2]
			}
		}
	}
	return ""
}

// WaitVMIP attend qu'une VM ait une adresse (timeout en secondes).
func (l *Lab) WaitVMIP(name string, timeout int) string {
	deadline := time.Now().Add(time.Duration(timeout) * time.Second)
	for {
		if ip := l.VMIP(name); ip != "" {
			return ip
		}
		if time.Now().After(deadline) {
			return ""
		}
		time.Sleep(3 * time.Second)
	}
}

// LabMarker est la marque des VMs créées par les scripts (description libvirt).
const LabMarker = "managed-by=labs-scripts"

// VMLab extrait le lab d'une description libvirt (« lab=X »).
func VMLab(desc string) string {
	for _, f := range strings.Fields(desc) {
		if strings.HasPrefix(f, "lab=") {
			return strings.TrimPrefix(f, "lab=")
		}
	}
	return ""
}
