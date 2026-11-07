package lab

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"labs/labctl/internal/coreconf"
	"labs/labctl/internal/virt/virtfake"
)

// Un module déposé dans modules/ mais jamais validé ni lancé ne doit rien pouvoir injecter dans la configuration
// de la passerelle : il est écarté (avec la raison), les autres modules continuent d'être servis.
func TestCoreInputSkipsUnsafeModule(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "config.env"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	write := func(name, yaml string) {
		dir := filepath.Join(root, "modules", name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "module.yaml"), []byte(yaml), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("good", "module: good\nnetwork: {subnet: 10.70.70.0/24}\nvms:\n  srv: {ip: 10}\n")
	// Nom de VM piégé (clé YAML entre guillemets) : fermerait la chaîne nft et réécrirait le pare-feu
	write("evil", "module: evil\nnetwork: {subnet: 10.71.71.0/24}\nvms:\n  \"a\\n}\\nflush chain inet gw forward\\n\": {ip: 10}\n")
	t.Setenv("LABS_PKI_DOMAIN", "omelab.internal")
	l := &Lab{Root: root, Virt: virtfake.New()}

	in, err := l.CoreInput()
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, m := range in.Modules {
		names = append(names, m.Name)
	}
	if strings.Join(names, ",") != "good" {
		t.Fatalf("modules servis : %v (evil devrait être écarté)", names)
	}
	if len(in.Skipped) != 1 || !strings.Contains(in.Skipped[0], "evil") {
		t.Fatalf("raison attendue pour evil : %v", in.Skipped)
	}
	files, err := coreconf.Render(in)
	if err != nil {
		t.Fatal(err)
	}
	for name, content := range files {
		if strings.Contains(content, "flush chain") {
			t.Fatalf("injection présente dans %s", name)
		}
	}
}
