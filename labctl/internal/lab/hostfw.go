package lab

import "os/exec"

// HostFWUnit : service systemd qui charge le pare-feu de l'hôte (labctl hostfw on). Sans lui, les VMs des modules
// peuvent joindre les services de ce poste par l'adresse .1 de leur réseau.
const HostFWUnit = "labs-hostfw.service"

// HostFirewallActive dit si le pare-feu de l'hôte est actif (sans droits root : état du service).
func HostFirewallActive() bool {
	return exec.Command("systemctl", "is-active", "--quiet", HostFWUnit).Run() == nil
}

// VulnerableModules renvoie les modules marqués vulnerable: true.
func (l *Lab) VulnerableModules() []string {
	mods, _ := l.Modules()
	var out []string
	for _, m := range mods {
		if m.Vulnerable() {
			out = append(out, m.Name)
		}
	}
	return out
}

// hostfwWarning : avertissement si des cibles vulnérables tournent sans le pare-feu de l'hôte ("" sinon).
func hostfwWarning(modules []string) string {
	if len(modules) == 0 || HostFirewallActive() {
		return ""
	}
	return "pare-feu de l'hôte INACTIF alors que des modules vulnérables existent : leurs cibles peuvent joindre " +
		"les services de ce poste (adresse .1 de leur réseau) -> sudo labctl hostfw on (chemin complet de labctl)"
}
