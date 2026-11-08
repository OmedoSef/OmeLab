package cli

import (
	"encoding/xml"
	"net/netip"
	"strings"
	"testing"

	"labs/labctl/internal/virt"
)

// La définition d'un réseau reste du XML bien formé, avec l'hôte en .1 et le DHCP sur la 2e moitié de la plage, et
// une valeur qui contiendrait du XML y reste du texte.
func TestNetworkXML(t *testing.T) {
	x := networkXML("demo", "nat", "demo.omelab.internal", netip.MustParsePrefix("10.10.0.0/24"), true)
	var n virt.Network
	virt.NetworkFromXML(&n, x)
	if n.Forward != "nat" || n.Domain != "demo.omelab.internal" || n.HostIP != "10.10.0.1" || n.Prefix != 24 || !n.DHCP {
		t.Fatalf("réseau relu : %+v\n%s", n, x)
	}
	if !strings.Contains(x, "<range start='10.10.0.128' end='10.10.0.254'/>") {
		t.Errorf("plage DHCP :\n%s", x)
	}
	piege := networkXML("a</name><forward mode='nat'/><name>b", "isolated", "x'/><forward mode='nat", netip.MustParsePrefix("10.11.0.0/24"), false)
	var doc struct {
		Name    string     `xml:"name"`
		Forward []struct{} `xml:"forward"`
	}
	if err := xml.Unmarshal([]byte(piege), &doc); err != nil || len(doc.Forward) != 0 {
		t.Errorf("XML injecté : %v, %d forward\n%s", err, len(doc.Forward), piege)
	}
}
