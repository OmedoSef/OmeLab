package cli

import (
	_ "embed"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
)

// Gabarits écrits par « labctl init », embarqués dans le binaire (il n'a pas besoin de l'arborescence source).
var (
	//go:embed templates/config.env
	tmplConfigEnv string
	//go:embed templates/lab.yaml
	tmplLabYAML string
	//go:embed templates/gitignore
	tmplGitignore string
)

func initCmd() *cobra.Command {
	var force bool
	c := &cobra.Command{
		Use:     "init [DOSSIER]",
		Short:   "Crée l'arborescence d'un nouveau lab (config.env, lab.yaml, modules/) ; défaut : dossier courant",
		GroupID: "outils",
		Args:    cobra.MaximumNArgs(1),
		Example: "  labctl init ~/mon-lab",
		RunE: func(cmd *cobra.Command, args []string) error {
			dir := "."
			if len(args) == 1 {
				dir = args[0]
			}
			return runInit(cmd.OutOrStdout(), dir, force)
		},
	}
	c.Flags().BoolVar(&force, "force", false, "continue même si le dossier contient déjà un lab (n'écrase jamais un fichier existant)")
	return c
}

// runInit crée l'arborescence d'un lab dans dir. Il n'écrase aucun fichier existant : un fichier déjà présent
// est laissé tel quel. Si dir est déjà un lab (config.env + modules/), on refuse sauf --force.
func runInit(out io.Writer, dir string, force bool) error {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		return fmt.Errorf("création de %s : %w", abs, err)
	}
	_, errCfg := os.Stat(filepath.Join(abs, "config.env"))
	_, errMods := os.Stat(filepath.Join(abs, "modules"))
	if errCfg == nil && errMods == nil && !force {
		return fmt.Errorf("%s est déjà un lab (config.env et modules/ présents) ; --force pour compléter ce qui manque", abs)
	}

	// Dossier modules/ (avec un .gitkeep pour qu'il survive à un commit même vide)
	if err := os.MkdirAll(filepath.Join(abs, "modules"), 0o755); err != nil {
		return err
	}
	files := []struct{ path, content string }{
		{"config.env", tmplConfigEnv},
		{"lab.yaml", tmplLabYAML},
		{".gitignore", tmplGitignore},
		{filepath.Join("modules", ".gitkeep"), ""},
	}
	for _, f := range files {
		full := filepath.Join(abs, f.path)
		if _, err := os.Stat(full); err == nil {
			fmt.Fprintf(out, "  %-22s existe déjà, inchangé\n", f.path)
			continue
		}
		if err := os.WriteFile(full, []byte(f.content), 0o644); err != nil {
			return fmt.Errorf("écriture de %s : %w", f.path, err)
		}
		fmt.Fprintf(out, "  %-22s créé\n", f.path)
	}

	fmt.Fprintf(out, "\nLab prêt dans %s\n", abs)
	fmt.Fprintf(out, "Prochaines étapes :\n")
	fmt.Fprintf(out, "  cd %s\n", dir)
	fmt.Fprintf(out, "  # déposer des modules dans modules/\n")
	fmt.Fprintf(out, "  # ajuster lab.yaml (ISO, mot de passe, domaine…) pour tous les modules à la fois\n")
	fmt.Fprintf(out, "  labctl pki init        # (optionnel) CA du lab : les VMs créées lui feront confiance\n")
	fmt.Fprintf(out, "  labctl status\n")
	fmt.Fprintf(out, "\nlabctl trouve ce lab s'il est lancé depuis ce dossier, ou via LABS_ROOT=%s\n", abs)
	return nil
}
