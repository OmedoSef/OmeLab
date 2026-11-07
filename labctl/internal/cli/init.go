package cli

import (
	"bufio"
	_ "embed"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/spf13/cobra"

	"labs/labctl/internal/lab"
	"labs/labctl/internal/module"
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
	var id identity
	c := &cobra.Command{
		Use:   "init [DOSSIER]",
		Short: "Crée l'arborescence d'un nouveau lab (config.env, lab.yaml, modules/) ; défaut : dossier courant",
		Long: `Crée l'arborescence d'un nouveau lab. L'identité du lab — organisation (nom des CA) et domaine racine —
est demandée dans un terminal (Entrée : ` + lab.DefaultOrg + `, ` + lab.DefaultDomain + `), ou donnée par --org et
--domain. Elle est écrite dans config.env, modifiable ensuite tant que « labctl pki init » n'a pas été lancé.`,
		GroupID: "outils",
		Args:    cobra.MaximumNArgs(1),
		Example: "  labctl init ~/mon-lab\n  labctl init ~/mon-lab --org \"ACME Lab\" --domain acme.internal",
		RunE: func(cmd *cobra.Command, args []string) error {
			dir := "."
			if len(args) == 1 {
				dir = args[0]
			}
			// Questions seulement dans un terminal, et pour ce que les options n'ont pas fixé
			fi, err := os.Stdin.Stat()
			tty := err == nil && fi.Mode()&os.ModeCharDevice != 0
			if null, err := os.Stat(os.DevNull); tty && err == nil && os.SameFile(fi, null) {
				tty = false // < /dev/null (scripts, CI) : un périphérique caractère, mais pas un terminal
			}
			id.askOrg = tty && !cmd.Flags().Changed("org")
			id.askDomain = tty && !cmd.Flags().Changed("domain")
			return runInit(cmd.OutOrStdout(), os.Stdin, dir, force, id)
		},
	}
	c.Flags().BoolVar(&force, "force", false, "continue même si le dossier contient déjà un lab (n'écrase jamais un fichier existant)")
	c.Flags().StringVar(&id.org, "org", lab.DefaultOrg, "organisation du lab : nom des CA (« <org> Root CA »), propriétaire des VMs Windows")
	c.Flags().StringVar(&id.domain, "domain", lab.DefaultDomain, "domaine racine des labs (sous-domaines par module : <module>.<domaine>)")
	return c
}

// identity : identité d'un nouveau lab (organisation, domaine racine), et ce qu'il faut demander.
type identity struct {
	org, domain       string
	askOrg, askDomain bool
}

var reRootDomain = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]*[a-z0-9])?\.)+[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)

// checkOrg : l'organisation entre dans le nom des CA (« <org> Intermediate CA », 64 caractères au plus en
// X.509) et dans une ligne VAR="…" de config.env.
func checkOrg(s string) error {
	switch {
	case s == "":
		return fmt.Errorf("organisation vide")
	case len(s) > 40:
		return fmt.Errorf("organisation trop longue (40 caractères au plus : elle entre dans le nom des CA)")
	case strings.ContainsAny(s, "\"$`\\\n"):
		return fmt.Errorf("organisation : pas de \" $ ` \\ ni de retour à la ligne")
	}
	return nil
}

func checkDomain(s string) error {
	if !reRootDomain.MatchString(s) {
		return fmt.Errorf("domaine invalide %q (ex. %s : minuscules, chiffres, tirets, au moins un point)", s, lab.DefaultDomain)
	}
	return nil
}

// ask pose une question avec une valeur par défaut (Entrée) et redemande tant que la réponse est refusée.
func ask(r *bufio.Reader, out io.Writer, label, def string, check func(string) error) (string, error) {
	for {
		fmt.Fprintf(out, "%s [%s] : ", label, def)
		line, rerr := r.ReadString('\n')
		v := strings.TrimSpace(line)
		if v == "" {
			v = def
		}
		err := check(v)
		if err == nil {
			return v, nil
		}
		fmt.Fprintf(out, "  %v\n", err)
		if rerr != nil { // fin de l'entrée : pas de seconde chance
			return "", err
		}
	}
}

// setDefault remplace la valeur par défaut d'une ligne VAR="${VAR:-défaut}" de config.env.
func setDefault(env, key, val string) string {
	re := regexp.MustCompile(`(?m)^` + key + `="\$\{` + key + `:-[^}]*\}"`)
	return re.ReplaceAllLiteralString(env, key+`="${`+key+`:-`+val+`}"`)
}

// labSSHKey génère la paire de clés SSH du lab (.ssh/id_ed25519, sans phrase de passe : labctl s'en sert sans
// invite), injectée dans les VMs à leur création. Si ssh-keygen manque ou échoue, le lab garde la clé personnelle
// par défaut (~/.ssh/id_ed25519.pub) : on le dit, sans échouer.
func labSSHKey(out io.Writer, root string) error {
	dir := filepath.Join(root, ".ssh")
	key := filepath.Join(dir, "id_ed25519")
	if _, err := os.Stat(key); err == nil {
		fmt.Fprintf(out, "  %-22s existe déjà, inchangé\n", ".ssh/id_ed25519")
		return nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	cmd := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", "labctl "+filepath.Base(root), "-f", key)
	if b, err := cmd.CombinedOutput(); err != nil {
		fmt.Fprintf(out, "  attention : clé SSH du lab non créée (%v %s) : la clé personnelle sera utilisée\n", err, strings.TrimSpace(string(b)))
		return err
	}
	fmt.Fprintf(out, "  %-22s créé (clé SSH du lab, injectée dans ses VMs)\n", ".ssh/id_ed25519")
	return nil
}

// runInit crée l'arborescence d'un lab dans dir. Il n'écrase aucun fichier existant : un fichier déjà présent
// est laissé tel quel. Si dir est déjà un lab (config.env + modules/), on refuse sauf --force. L'identité
// (organisation, domaine) n'est demandée et écrite que si config.env est créé ici.
func runInit(out io.Writer, in io.Reader, dir string, force bool, id identity) error {
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

	// Identité du lab : demandée seulement si config.env va être créé (sinon on garde celle du fichier existant)
	newConfig := errCfg != nil
	if newConfig {
		if err := checkOrg(id.org); err != nil {
			return err
		}
		id.domain = strings.ToLower(id.domain)
		if err := checkDomain(id.domain); err != nil {
			return err
		}
		if id.askOrg || id.askDomain {
			fmt.Fprintln(out, "Identité du lab (Entrée : valeur proposée ; modifiable ensuite dans config.env, avant « labctl pki init ») :")
			r := bufio.NewReader(in)
			var err error
			if id.askOrg {
				if id.org, err = ask(r, out, "  Organisation (nom des CA)", id.org, checkOrg); err != nil {
					return err
				}
			}
			if id.askDomain {
				if id.domain, err = ask(r, out, "  Domaine racine", id.domain, func(s string) error { return checkDomain(strings.ToLower(s)) }); err != nil {
					return err
				}
				id.domain = strings.ToLower(id.domain)
			}
		}
		if strings.HasSuffix(id.domain, ".local") {
			fmt.Fprintln(out, "  attention : .local est le domaine du mDNS (Avahi) ; préférer .internal")
		}
	}
	cfg := setDefault(setDefault(tmplConfigEnv, "LABS_PKI_ORG", id.org), "LABS_PKI_DOMAIN", id.domain)
	// Clé SSH propre au lab (pour accéder à ses VMs), plutôt que la clé personnelle de l'utilisateur
	if newConfig {
		if err := labSSHKey(out, abs); err == nil {
			cfg = setDefault(cfg, "LABS_SSH_PUBKEY", "$LABS_ROOT/.ssh/id_ed25519.pub")
		}
	}

	// Dossier modules/ (avec un .gitkeep pour qu'il survive à un commit même vide)
	if err := os.MkdirAll(filepath.Join(abs, "modules"), 0o755); err != nil {
		return err
	}
	files := []struct{ path, content string }{
		{"config.env", cfg},
		{"lab.yaml", tmplLabYAML},
		{".gitignore", tmplGitignore},
		{module.SchemaFile, string(module.SchemaJSON)}, // complétion et contrôle des module.yaml dans l'éditeur
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
	if newConfig {
		fmt.Fprintf(out, "Identité : organisation « %s », domaine %s (CA « %s Root CA », modules en <module>.%s)\n", id.org, id.domain, id.org, id.domain)
	}
	fmt.Fprintf(out, "Prochaines étapes :\n")
	fmt.Fprintf(out, "  cd %s\n", dir)
	fmt.Fprintf(out, "  # déposer des modules dans modules/\n")
	fmt.Fprintf(out, "  # ajuster lab.yaml (ISO, mot de passe, domaine…) pour tous les modules à la fois\n")
	fmt.Fprintf(out, "  labctl pki init        # (optionnel) CA du lab : les VMs créées lui feront confiance\n")
	fmt.Fprintf(out, "  labctl status\n")
	fmt.Fprintf(out, "\nlabctl trouve ce lab s'il est lancé depuis ce dossier, ou via LABS_ROOT=%s\n", abs)
	return nil
}
