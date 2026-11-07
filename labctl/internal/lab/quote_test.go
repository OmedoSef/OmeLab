package lab

import (
	"os/exec"
	"testing"
)

// shellQuote doit rendre n'importe quelle valeur inerte pour sh : ce qui ressort est exactement ce qui est entré.
func TestShellQuoteIsInert(t *testing.T) {
	for _, s := range []string{
		"/etc/ssl/a.crt", "a b", "$(id)", "`id`", "a'b", `a"b`, "a;rm -rf /x", "x\ny", "$HOME", "",
	} {
		out, err := exec.Command("sh", "-c", "printf %s "+shellQuote(s)).Output()
		if err != nil {
			t.Fatalf("%q : %v", s, err)
		}
		if string(out) != s {
			t.Errorf("%q devenu %q dans le shell", s, out)
		}
	}
}

// Une zone IPv6 arbitraire est refusée avant tout SSH vers la passerelle.
func TestPortalKickRejectsUnsafeAddress(t *testing.T) {
	g := &Gateway{}
	for _, ip := range []string{"fe80::1%$(id)", "fe80::1%a;touch /tmp/x", "10.0.0.1;id", "::1", ""} {
		if err := g.PortalKick(0, ip); err == nil {
			t.Errorf("adresse %q acceptée", ip)
		}
	}
}
