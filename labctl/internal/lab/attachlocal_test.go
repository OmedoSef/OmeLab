package lab

import (
	"os"
	"path/filepath"
	"testing"

	"labs/labctl/internal/module"
)

func TestLocalAttachRoundTrip(t *testing.T) {
	dir := t.TempDir()
	l := &Lab{Root: dir}

	// Ajout de deux branchements locaux sur des modules cibles différents.
	if err := l.AddLocalAttach("pjpt", "attacker/kali", 50); err != nil {
		t.Fatal(err)
	}
	if err := l.AddLocalAttach("pentest", "attacker/kali", 10); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "attach.local.yaml")); err != nil {
		t.Fatalf("attach.local.yaml non écrit : %v", err)
	}

	// Injection dans un module : seules les entrées de ce module, marquées Local.
	m := &module.Module{Module: "pjpt"}
	l.injectLocalAttach(m)
	if len(m.Attach) != 1 || m.Attach[0].VM != "attacker/kali" || m.Attach[0].IP != 50 || !m.Attach[0].Local {
		t.Fatalf("injection pjpt inattendue : %+v", m.Attach)
	}

	// La MAC écrite par un branchement revient à l'injection suivante.
	if err := l.setLocalAttachMAC("pjpt", "attacker/kali", "52:54:00:aa:bb:cc"); err != nil {
		t.Fatal(err)
	}
	m2 := &module.Module{Module: "pjpt"}
	l.injectLocalAttach(m2)
	if m2.Attach[0].MAC != "52:54:00:aa:bb:cc" {
		t.Fatalf("MAC non persistée : %+v", m2.Attach)
	}

	// Débrancher retire l'entrée et renvoie sa MAC ; le fichier disparaît quand il ne reste rien.
	mac, err := l.DelLocalAttach("pjpt", "attacker/kali")
	if err != nil {
		t.Fatal(err)
	}
	if mac != "52:54:00:aa:bb:cc" {
		t.Fatalf("MAC retournée : %q", mac)
	}
	m3 := &module.Module{Module: "pjpt"}
	l.injectLocalAttach(m3)
	if len(m3.Attach) != 0 {
		t.Fatalf("pjpt devrait être vide : %+v", m3.Attach)
	}
	// pentest reste branché.
	m4 := &module.Module{Module: "pentest"}
	l.injectLocalAttach(m4)
	if len(m4.Attach) != 1 {
		t.Fatalf("pentest devrait rester : %+v", m4.Attach)
	}
}
