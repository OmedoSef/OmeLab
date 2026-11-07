package module

import (
	"strings"
	"testing"
)

func issuesAt(t *testing.T, yml string, ctx Context) map[string]string {
	t.Helper()
	m, err := Parse([]byte(yml), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, i := range m.Validate(ctx) {
		if i.Error {
			out[i.Path] = i.Msg
		}
	}
	return out
}

// Un module vulnérable ne déclare rien de sortant.
func TestVulnerableModuleHasNoOutgoing(t *testing.T) {
	ctx := Context{RootDomain: "omelab.internal", Modules: []string{"pentest", "attacker"}}
	yml := `module: pentest
vulnerable: true
network: {subnet: 10.66.66.0/24}
dns: {links: [attacker]}
connections:
  - {to: internet, ports: [tcp/443]}
  - {to: attacker, ports: [tcp/4444]}
vms:
  web: {ip: 30, internet: install}
`
	errs := issuesAt(t, yml, ctx)
	for _, p := range []string{"connections.0", "connections.1", "dns.links"} {
		if !strings.Contains(errs[p], "module vulnérable") {
			t.Errorf("%s : erreur « module vulnérable » attendue, obtenu %q", p, errs[p])
		}
	}
	// internet: install reste permis (images Docker de pentest/web à l'installation)
	for p := range errs {
		if strings.Contains(p, "internet") {
			t.Errorf("internet: install ne doit pas être refusé : %s", p)
		}
	}
}

// Les VMs d'un module vulnérable ne se branchent pas sur le réseau d'un autre module.
func TestVulnerableVMNotAttachedElsewhere(t *testing.T) {
	ctx := Context{RootDomain: "omelab.internal", Modules: []string{"pentest", "sample"},
		Targets: map[string][]string{"pentest": {"web"}}, Vulnerable: []string{"pentest"}}
	yml := "module: sample\nnetwork: {subnet: 10.69.69.0/24}\nattach:\n  - {vm: pentest/web, ip: 50}\nvms:\n  srv: {ip: 10}\n"
	if errs := issuesAt(t, yml, ctx); !strings.Contains(errs["attach.0.vm"], "module vulnérable") {
		t.Errorf("branchement d'une cible ailleurs accepté : %v", errs)
	}
}
