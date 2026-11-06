package module

import (
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func ctx() Context {
	return Context{
		RootDomain: "omedo.lab", Images: []string{"debian13", "ubuntu2404"}, Modules: []string{"pentest", "demo", "attacker"},
		Targets: map[string][]string{"demo": {"srv", "web"}, "attacker": {"kali", "parrot"}},
		Used:    []NetInfo{{Owner: "module demo", Name: "demo-lab", Subnet: netip.MustParsePrefix("10.70.70.0/24"), Domain: "demo-lab.omedo.lab"}},
	}
}

// issuesFor charge le module pentest réel modifié par edit (texte), et renvoie les messages d'erreur.
func issuesFor(t *testing.T, edit func(string) string) []string {
	t.Helper()
	m := pentestCopy(t)
	text := edit(string(m.orig))
	os.WriteFile(filepath.Join(m.Dir, FileName), []byte(text), 0o644)
	m2, err := Load(m.Dir)
	if err != nil {
		return []string{"LECTURE: " + err.Error()}
	}
	var out []string
	for _, i := range m2.Validate(ctx()) {
		if i.Error {
			out = append(out, i.Path+" : "+i.Msg)
		}
	}
	return out
}

func TestValidatePentestOK(t *testing.T) {
	if errs := issuesFor(t, func(s string) string { return s }); len(errs) != 0 {
		t.Errorf("le module pentest réel doit être valide : %v", errs)
	}
}

func TestValidateErrors(t *testing.T) {
	cases := []struct {
		name, from, to, want string
	}{
		{"clé inconnue", "    cpus: 2\n", "    cpu: 2\n", "LECTURE: ligne"},
		{"type", "    cpus: 2\n", "    cpus: quatre\n", "LECTURE: ligne"},
		{"ip en double", "    ip: 30\n", "    ip: 20\n", "vms.web.ip : 20 déjà utilisé"},
		{"ip hors plage", "    ip: 30\n", "    ip: 150\n", "entre 2 et 99"},
		{"mac en double", "mac: 52:54:00:66:66:30", "mac: 52:54:00:66:66:20", "MAC déjà utilisée"},
		{"mac passerelle", "mac: 52:54:00:66:66:30", "mac: 52:54:00:fe:42:42", "réservé aux cartes de la passerelle"},
		{"chevauchement", "subnet: 10.66.66.0/24", "subnet: 10.70.70.0/24", "chevauche"},
		{"pas un /24", "subnet: 10.66.66.0/24", "subnet: 10.66.0.0/16", "/24"},
		{"image inconnue", "image: ubuntu2404", "image: windows", "image inconnue"},
		{"image et source", "    role: target\n    ip: 30", "    role: target\n    ip: 30\n    source: {url: https://x/y.qcow2, checksum: 'sha256:00'}", "image et source"},
		{"empreinte mal formée", "md5:abb0a95bd4422397ed235a7284e2ed7f", "md5:abc", "longueur incorrecte"},
		{"paquets sans cloud-init", "    internet: never", "    internet: never\n    packages: [curl]", "image cloud"},
		{"alias vers VM inconnue", "juice: web", "juice: webb", "cible inconnue"},
		{"lien inconnu", "dns:\n", "dns:\n  links: [absent]\n", "module inconnu"},
		{"variable inconnue", "      - docker compose", "      - echo ${vm.adresse} && docker compose", "champ de VM inconnu"},
		{"fichier absent", "src: files/compose.yaml", "src: files/absent.yaml", "fichier introuvable"},
		{"certificat hors domaine", "dns: [dvwa, juice]", "dns: [dvwa, www.google.com]", "hors du domaine"},
		{"internet invalide", "internet: install", "internet: parfois", "valeur \"parfois\" invalide"},
		{"nom du module", "module: pentest", "module: autre", "celui du dossier"},
		{"connexion : port", "dns:\n", "connections:\n  - {to: demo, ports: [tcp/99999]}\ndns:\n", "port invalide"},
		{"connexion : cible", "dns:\n", "connections:\n  - {to: absent}\ndns:\n", "module inconnu"},
		{"connexion : soi-même", "dns:\n", "connections:\n  - {to: pentest}\ndns:\n", "relié à lui-même"},
		{"connexion : VM cible", "dns:\n", "connections:\n  - {to: demo/zz}\ndns:\n", "pas une VM à IP fixe"},
		{"connexion : source", "dns:\n", "connections:\n  - {to: internet, from: [nope]}\ndns:\n", "ni une VM ni une réservation"},
		{"connexion : via", "dns:\n", "connections:\n  - {to: internet, via: vpn}\ndns:\n", "via inconnu"},
		{"proxy : mode", "dns:\n", "connections:\n  - {to: internet, via: proxy, proxy: {mode: spy}}\ndns:\n", "mode inconnu"},
		{"proxy : filtre vide", "dns:\n", "connections:\n  - {to: internet, via: proxy, proxy: {mode: filter}}\ndns:\n", "demande allow"},
		{"proxy : allow sans filtre", "dns:\n", "connections:\n  - {to: internet, via: proxy, proxy: {allow: [a.com]}}\ndns:\n", "demandent mode: filter"},
		{"proxy : domaine", "dns:\n", "connections:\n  - {to: internet, via: proxy, proxy: {mode: filter, allow: ['http://x']}}\ndns:\n", "domaine invalide"},
		{"proxy : port", "dns:\n", "connections:\n  - {to: internet, via: proxy, proxy: {ports: [udp/53]}}\ndns:\n", "port TCP attendu"},
		{"attach : format", "vms:\n", "attach:\n  - {vm: kali, ip: 50}\nvms:\n", "attendu <module>/<vm>"},
		{"attach : module", "vms:\n", "attach:\n  - {vm: absent/kali, ip: 50}\nvms:\n", "module inconnu"},
		{"attach : VM", "vms:\n", "attach:\n  - {vm: demo/zz, ip: 50}\nvms:\n", "pas une VM à IP fixe"},
		{"attach : IP prise", "vms:\n", "attach:\n  - {vm: demo/srv, ip: 20}\nvms:\n", "20 déjà utilisé"},
		{"attach : nom pris", "vms:\n", "attach:\n  - {vm: demo/web, ip: 50}\nvms:\n", "déjà pris dans ce module"},
		{"attach : sans ip", "vms:\n", "attach:\n  - {vm: demo/srv}\nvms:\n", "ip obligatoire"},
		{"efi : secure boot", "    image: ubuntu2404\n", "    image: ubuntu2404\n    hardware: {secure_boot: true}\n", "Secure Boot demande firmware: efi"},
		{"proxy sans via", "dns:\n", "connections:\n  - {to: internet, proxy: {mode: log}}\ndns:\n", "sans via: proxy"},
		{"portail : auth", "dns:\n", "connections:\n  - {to: internet, via: portal, portal: {auth: sms}}\ndns:\n", "auth inconnu"},
		{"portail : mot de passe", "dns:\n", "connections:\n  - {to: internet, via: portal, portal: {auth: password}}\ndns:\n", "demande un password"},
		{"portail : durée", "dns:\n", "connections:\n  - {to: internet, via: portal, portal: {duration: 3d}}\ndns:\n", "durée invalide"},
		{"portail sans via", "dns:\n", "connections:\n  - {to: internet, portal: {auth: click}}\ndns:\n", "sans via: portal"},
		{"portail : never", "dns:\n", "connections:\n  - {to: internet, from: [msf2], via: portal}\ndns:\n", "internet: never"},
		{"connexion et lien", "dns:\n", "connections:\n  - {to: demo, ports: [tcp/80]}\ndns:\n  links: [demo]\n", "déjà dans dns.links"},
	}
	for _, c := range cases {
		errs := issuesFor(t, func(s string) string {
			if !strings.Contains(s, c.from) {
				t.Fatalf("%s : motif %q absent du module réel", c.name, c.from)
			}
			return strings.Replace(s, c.from, c.to, 1)
		})
		if !strings.Contains(strings.Join(errs, "\n"), c.want) {
			t.Errorf("%s : erreur contenant %q attendue, obtenu %v", c.name, c.want, errs)
		}
	}
}

func TestEffectiveDefaults(t *testing.T) {
	m, err := Parse([]byte("module: demo\ndefaults: {memory: 1536, image: ubuntu2404}\nvms:\n  a: {}\n  b: {cpus: 3, memory: 4096}\n"), "/x/demo")
	if err != nil {
		t.Fatal(err)
	}
	set := func(k, f string) string {
		if k == "LABS_DEFAULT_DISK" {
			return "30"
		}
		return f
	}
	a, b := m.Effective("a", set), m.Effective("b", set)
	if a.CPUs != 1 || a.Origins["cpus"] != FromBuiltin || a.Memory != 1536 || a.Origins["memory"] != FromDefaults ||
		a.Disk != 30 || a.Origins["disk"] != FromSettings || a.Image != "ubuntu2404" || !a.Agent {
		t.Errorf("a : %+v", a)
	}
	if b.CPUs != 3 || b.Memory != 4096 || b.Origins["memory"] != FromVM {
		t.Errorf("b : %+v", b)
	}
	if m.NetworkName() != "demo-lab" || m.Domain("omedo.lab") != "demo-lab.omedo.lab" {
		t.Errorf("réseau par défaut : %s %s", m.NetworkName(), m.Domain("omedo.lab"))
	}
}
