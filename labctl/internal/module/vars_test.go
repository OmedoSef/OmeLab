package module

import "testing"

func TestExpandVars(t *testing.T) {
	m := &Module{Module: "pjpt", Vars: map[string]string{"domain": "pjptlab.local", "pw": "S3cr3t!"}}
	c := VarContext{Module: m}

	got, err := c.Expand("Domaine ${var.domain}, mdp ${var.pw}")
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	if want := "Domaine pjptlab.local, mdp S3cr3t!"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}

	if _, err := c.Expand("${var.absente}"); err == nil {
		t.Fatal("variable absente : erreur attendue")
	}
}
