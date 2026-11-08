package vmdef

import (
	"encoding/xml"
	"strings"
	"testing"
)

func baseSpec() Spec {
	return Spec{Name: "web1", Image: "debian13", CPUs: 2, MemoryMiB: 1024, DiskGiB: 10,
		Nets: []NetSpec{{Network: "lab", Address: "10.10.0.11/24", MAC: "52:54:00:aa:bb:cc"}, {Network: "default", MAC: "52:54:00:aa:bb:dd"}}}
}

func TestValidate(t *testing.T) {
	if s := baseSpec(); s.Validate() != nil {
		t.Fatal(s.Validate())
	}
	bad := map[string]func(*Spec){
		"nom":        func(s *Spec) { s.Name = "a b" },
		"image":      func(s *Spec) { s.Image = "windows" },
		"mémoire":    func(s *Spec) { s.MemoryMiB = 64 },
		"réseau":     func(s *Spec) { s.Nets = nil },
		"masque":     func(s *Spec) { s.Nets[0].Address = "10.10.0.11" },
		"mac":        func(s *Spec) { s.Nets[0].MAC = "zz" },
		"fichier":    func(s *Spec) { s.Files = []FileSpec{{Src: "a", Dest: "relatif"}} },
		"mode":       func(s *Spec) { s.Files = []FileSpec{{Src: "a", Dest: "/x", Mode: "999"}} },
		"passerelle": func(s *Spec) { s.Gateway = "pas-une-ip" },
	}
	for what, mutate := range bad {
		s := baseSpec()
		mutate(&s)
		if s.Validate() == nil {
			t.Errorf("%s : doit être refusé", what)
		}
	}
}

func TestRenderCloudInit(t *testing.T) {
	s := baseSpec()
	s.Packages = []string{"curl"}
	s.Run = []string{`echo "a b" > /x`}
	s.Files = []FileSpec{{Src: "local", Dest: "/etc/x", Mode: "0600"}}
	ci := RenderCloudInit(&s, CloudEnv{Domain: "lab.omedo.lab", Gateway: "10.10.0.1", Keyboard: "fr", User: "lab", Password: "pw",
		PubKey: "ssh-ed25519 AAA test\n", RootCA: "-----BEGIN CERTIFICATE-----\nAAA\n-----END CERTIFICATE-----\n",
		Files: map[string][]byte{"local": []byte("bonjour")}, InstantID: "web1-1"})
	for _, want := range []string{
		"fqdn: web1.lab.omedo.lab", "packages: [qemu-guest-agent, curl]", `  - [sh, -c, "echo \"a b\" > /x"]`,
		"  - path: /etc/x\n    permissions: '0600'\n    encoding: b64\n    content: Ym9uam91cg==",
		"      - ssh-ed25519 AAA test\n", "ca_certs:\n  trusted:\n    - |\n      -----BEGIN CERTIFICATE-----\n      AAA\n",
	} {
		if !strings.Contains(ci.UserData, want) {
			t.Errorf("user-data : %q absent\n%s", want, ci.UserData)
		}
	}
	for _, want := range []string{"addresses: [10.10.0.11/24]", "{to: default, via: 10.10.0.1}", "nameservers: {addresses: [10.10.0.1]}",
		"eth1:\n    match: {macaddress: \"52:54:00:aa:bb:dd\"}\n    set-name: eth1\n    dhcp4: true"} {
		if !strings.Contains(ci.NetworkConfig, want) {
			t.Errorf("network-config : %q absent\n%s", want, ci.NetworkConfig)
		}
	}
	s.NoCA = true
	if strings.Contains(RenderCloudInit(&s, CloudEnv{RootCA: "x"}).UserData, "ca_certs") {
		t.Error("--no-ca : pas de CA")
	}
	ud := RenderCloudInit(&s, CloudEnv{UserData: []byte("#cloud-config\nperso: true\n")}).UserData
	if ud != "#cloud-config\nperso: true\n" {
		t.Errorf("user-data fourni : remplacé tel quel attendu, obtenu %q", ud)
	}
}

func TestRenderDomainXML(t *testing.T) {
	s := baseSpec()
	x := RenderDomainXML(&s, "/vms/web1/disk.qcow2", "/vms/web1/seed.iso", "managed-by=labs-scripts lab=a&b")
	var d struct {
		Name string `xml:"name"`
		Desc string `xml:"description"`
		Mem  int    `xml:"memory"`
		Nets []struct {
			MAC struct {
				Address string `xml:"address,attr"`
			} `xml:"mac"`
			Src struct {
				Network string `xml:"network,attr"`
			} `xml:"source"`
		} `xml:"devices>interface"`
	}
	if err := xml.Unmarshal([]byte(x), &d); err != nil {
		t.Fatalf("XML invalide : %v\n%s", err, x)
	}
	if d.Name != "web1" || d.Desc != "managed-by=labs-scripts lab=a&b" || d.Mem != 1024 || len(d.Nets) != 2 || d.Nets[0].Src.Network != "lab" {
		t.Errorf("définition : %+v", d)
	}
	for _, want := range []string{"org.qemu.guest_agent.0", "host-passthrough", "machine='q35'", "address='127.0.0.1'", "<source file='/vms/web1/disk.qcow2'/>"} {
		if !strings.Contains(x, want) {
			t.Errorf("%q absent", want)
		}
	}
}
