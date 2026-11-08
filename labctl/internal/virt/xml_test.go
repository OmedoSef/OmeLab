package virt

import (
	"os"
	"slices"
	"strings"
	"testing"
)

func sampleDomainXML(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("testdata/domain.xml") // forme d'un « virsh dumpxml --inactive » (VM Windows UEFI)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestResourcesFromXML(t *testing.T) {
	cpus, mem, err := ResourcesFromXML(sampleDomainXML(t))
	if err != nil || cpus != 2 || mem != 4096 {
		t.Fatalf("%d vCPU, %d Mio, %v", cpus, mem, err)
	}
	for _, c := range []struct {
		unit, value string
	}{{"KiB", "2097152"}, {"MiB", "2048"}, {"GiB", "2"}, {"b", "2147483648"}} {
		x := "<domain><vcpu>1</vcpu><memory unit='" + c.unit + "'>" + c.value + "</memory></domain>"
		if _, mem, err := ResourcesFromXML(x); err != nil || mem != 2048 {
			t.Errorf("%s %s : %d Mio, %v", c.value, c.unit, mem, err)
		}
	}
	if _, _, err := ResourcesFromXML("pas du xml"); err == nil {
		t.Error("XML illisible accepté")
	}
}

// Les ressources réécrites sont relues à l'identique ; le reste de la définition ne bouge pas.
func TestWithResources(t *testing.T) {
	x := sampleDomainXML(t)
	y := withResources(x, 4, 8192)
	cpus, mem, err := ResourcesFromXML(y)
	if err != nil || cpus != 4 || mem != 8192 {
		t.Fatalf("après réécriture : %d vCPU, %d Mio, %v", cpus, mem, err)
	}
	if !strings.Contains(y, "<currentMemory unit='KiB'>8388608</currentMemory>") || !strings.Contains(y, "<vcpu placement='static'>4</vcpu>") {
		t.Errorf("mémoire courante ou placement perdus :\n%s", y)
	}
	if withResources(y, 2, 4096) != x {
		t.Error("aller-retour : la définition d'origine n'est pas retrouvée")
	}
}

// Les lecteurs de CD et le démarrage sur CD disparaissent ; disque, cartes et firmware restent.
func TestWithoutCDROMs(t *testing.T) {
	y := withoutCDROMs(sampleDomainXML(t))
	for _, gone := range []string{"device='cdrom'", "Win11.iso", "virtio-win.iso", "<boot dev='cdrom'/>"} {
		if strings.Contains(y, gone) {
			t.Errorf("%s encore présent", gone)
		}
	}
	for _, kept := range []string{"disk.qcow2", "<boot dev='hd'/>", "pjpt-lab", "OVMF_CODE_4M"} {
		if !strings.Contains(y, kept) {
			t.Errorf("%s perdu", kept)
		}
	}
	if ifs, err := InterfacesFromXML(y); err != nil || len(ifs) != 2 {
		t.Errorf("définition abîmée : %v %v", ifs, err)
	}
}

func TestInterfacesFromXML(t *testing.T) {
	ifs, err := InterfacesFromXML(sampleDomainXML(t))
	if err != nil {
		t.Fatal(err)
	}
	want := []Interface{
		{Type: "network", Network: "pjpt-lab", MAC: "52:54:00:ab:cd:01", Model: "e1000"}, // MAC ramenée en minuscules
		{Type: "bridge", MAC: "52:54:00:ab:cd:02", Model: "virtio"},
	}
	if !slices.Equal(ifs, want) {
		t.Errorf("cartes : %+v", ifs)
	}
}

func TestNetworkFromXML(t *testing.T) {
	var n Network
	NetworkFromXML(&n, `<network><name>lab-x</name><forward mode='nat'/><bridge name='virbr9'/><domain name='x.omelab.internal'/>
<ip address='10.9.0.1' netmask='255.255.255.0'><dhcp><range start='10.9.0.128' end='10.9.0.254'/></dhcp></ip></network>`)
	if n != (Network{Bridge: "virbr9", Forward: "nat", Domain: "x.omelab.internal", HostIP: "10.9.0.1", Prefix: 24, DHCP: true}) {
		t.Errorf("réseau : %+v", n)
	}
	var iso Network
	NetworkFromXML(&iso, `<network><name>m-lab</name><bridge name='virbr-m'/><ip address='10.66.66.1' prefix='24'/></network>`)
	if iso.Forward != "" || iso.Prefix != 24 || iso.DHCP {
		t.Errorf("réseau isolé : %+v", iso)
	}
	keep := Network{Name: "garde"}
	NetworkFromXML(&keep, "illisible")
	if keep != (Network{Name: "garde"}) {
		t.Errorf("définition illisible : %+v", keep)
	}
}
