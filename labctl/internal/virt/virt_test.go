package virt

import (
	"net/netip"
	"testing"
)

// Une VM ne peut pas faire viser à labctl une autre adresse que celles de ses propres réseaux.
func TestPickIPv4(t *testing.T) {
	ifaces := []Interface{
		{Type: "network", Network: "pentest-lab", MAC: "52:54:00:66:66:30"},
		{Type: "network", Network: "attacker-lab", MAC: "52:54:00:68:68:10"},
		{Type: "network", Network: "attacker-lab", MAC: "52:54:00:ab:cd:ef"},
	}
	nets := map[string]netip.Prefix{
		"pentest-lab":  netip.MustParsePrefix("10.66.66.1/24"), // adresse de l'hôte, non masquée (comme IPv4)
		"attacker-lab": netip.MustParsePrefix("10.68.68.1/24"),
	}
	cases := []struct {
		name  string
		cands []Address
		want  string
	}{
		{"adresse normale", []Address{{MAC: "52:54:00:66:66:30", IP: "10.66.66.30"}}, "10.66.66.30"},
		{"IP publique annoncée par l'agent", []Address{{MAC: "52:54:00:66:66:30", IP: "203.0.113.7"}}, ""},
		{"IP d'un autre réseau sur cette carte", []Address{{MAC: "52:54:00:66:66:30", IP: "10.68.68.10"}}, ""},
		{"MAC inconnue", []Address{{MAC: "52:54:00:de:ad:00", IP: "10.66.66.40"}}, ""},
		{"la première valide l'emporte", []Address{
			{MAC: "52:54:00:66:66:30", IP: "192.168.1.10"},
			{MAC: "52:54:00:68:68:10", IP: "10.68.68.10"},
		}, "10.68.68.10"},
		{"MAC en majuscules", []Address{{MAC: "52:54:00:AB:CD:EF", IP: "10.68.68.11"}}, "10.68.68.11"},
		{"adresse de l'hôte", []Address{{MAC: "52:54:00:66:66:30", IP: "10.66.66.1"}}, ""},
		{"adresse du réseau", []Address{{MAC: "52:54:00:66:66:30", IP: "10.66.66.0"}}, ""},
		{"rien", nil, ""},
	}
	for _, c := range cases {
		if got := pickIPv4(c.cands, ifaces, nets); got != c.want {
			t.Errorf("%s : %q, attendu %q", c.name, got, c.want)
		}
	}
	// Pont direct (aucun réseau libvirt connu) : ancien comportement, première adresse
	bridge := []Interface{{Type: "bridge", MAC: "52:54:00:aa:bb:cc"}}
	if got := pickIPv4([]Address{{MAC: "52:54:00:aa:bb:cc", IP: "192.168.1.50"}}, bridge, nets); got != "192.168.1.50" {
		t.Errorf("pont direct : %q", got)
	}
}
