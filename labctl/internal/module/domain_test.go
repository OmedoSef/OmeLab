package module

import "testing"

// Le domaine d'un module : sous la racine du lab ou sous un suffixe privé, jamais un domaine d'Internet (la PKI
// émettrait des certificats pour lui, et le DNS du lab le détournerait).
func TestPrivateDomain(t *testing.T) {
	for _, c := range []struct {
		domain string
		ok     bool
	}{
		{"pentest.omedo.lab", true}, {"omedo.lab", true}, {"a.b.omedo.lab", true},
		{"pjptlab.local", true}, {"corp.internal", true}, {"x.home.arpa", true}, {"Labo.Test", true},
		{"github.com", false}, {"www.github.com", false}, {"debian.org", false}, {"internal", false},
		{"omedo.lab.evil.com", false}, {"xomedo.lab", true /* .lab est privé */}, {"evil-internal.com", false}, {"", false},
	} {
		if got := PrivateDomain(c.domain, "omedo.lab"); got != c.ok {
			t.Errorf("%q : %v, attendu %v", c.domain, got, c.ok)
		}
	}
	if !PrivateDomain("lab.acme.com", "acme.com") || PrivateDomain("acme.org", "acme.com") {
		t.Error("racine publique choisie par l'opérateur : ses sous-domaines seulement")
	}
}
