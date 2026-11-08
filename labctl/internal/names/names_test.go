package names

import "testing"

func TestRules(t *testing.T) {
	for _, c := range []struct {
		rule func(string) bool
		name string
		ok   []string
		ko   []string
	}{
		{Label, "Label", []string{"web", "a", "pjpt-lab", "dc01"}, []string{"", "Web", "-a", "a-", "a.b", "a b", "a\nb", "a/b", "é"}},
		{Domain, "Domain", []string{"omelab.internal", "a.b.c", "pjptlab.local"}, []string{"local", "a..b", ".a.b", "A.b", "a.b.", "a_b.c", "a.b\n"}},
		{MAC, "MAC", []string{"52:54:00:ab:cd:ef"}, []string{"52:54:00:AB:CD:EF", "52-54-00-ab-cd-ef", "52:54:00:ab:cd", "52:54:00:ab:cd:ef:00"}},
		{Host, "Host", []string{"Web1", "dc01", "A"}, []string{"", "-a", "a-", "a.b", "a_b"}},
	} {
		for _, s := range c.ok {
			if !c.rule(s) {
				t.Errorf("%s(%q) refusé", c.name, s)
			}
		}
		for _, s := range c.ko {
			if c.rule(s) {
				t.Errorf("%s(%q) accepté", c.name, s)
			}
		}
	}
}
