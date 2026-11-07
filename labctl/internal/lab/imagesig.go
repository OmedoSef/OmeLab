package lab

import (
	"embed"
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Signature des fichiers d'empreintes des images du catalogue. HTTPS et l'empreinte protègent du transport, pas
// d'un miroir compromis qui servirait une image et le fichier d'empreintes qui va avec : quand l'éditeur signe ce
// fichier, sa signature est vérifiée (gpgv) avec sa clé officielle, embarquée ici, avant de faire confiance à
// l'empreinte. Debian ne signe pas ses images cloud : elles restent vérifiées par HTTPS + empreinte seulement.
//
// Clés (keys/*.asc), chacune contrôlée à l'ajout en validant les signatures réellement publiées :
//
//	ubuntu.asc  D2EB 4462 6FDD C30B 513D  5BB7 1A5D 6C4C 7DB8 7C81  UEC Image Automatic Signing Key <cdimage@ubuntu.com>
//	rocky.asc   21CB 256A E16F C54C 6E65  2949 702D 426D 350D 275D  Rocky Enterprise Software Foundation - Release key 2022
//	alma.asc    BF18 AC28 7617 8908 D6E7  1267 D36C B86C B86B 3716  AlmaLinux OS 9 <packager@almalinux.org>
//
// Si un éditeur change de clé, le téléchargement échoue (signature invalide) jusqu'à la mise à jour de labctl :
// c'est voulu, on ne fait jamais confiance à une clé inconnue.

//go:embed keys/*.asc
var signingKeys embed.FS

// dearmor convertit une clé publique OpenPGP « armurée » (texte) en binaire, forme qu'attend gpgv.
func dearmor(armored []byte) ([]byte, error) {
	var b64 strings.Builder
	in, body := false, false
	for _, line := range strings.Split(strings.ReplaceAll(string(armored), "\r", ""), "\n") {
		switch {
		case strings.HasPrefix(line, "-----BEGIN PGP PUBLIC KEY BLOCK-----"):
			in, body = true, false
			continue
		case strings.HasPrefix(line, "-----END PGP PUBLIC KEY BLOCK-----"):
			in = false
		}
		switch {
		case !in:
		case !body: // en-têtes (Comment:, Version:) jusqu'à la ligne vide
			body = strings.TrimSpace(line) == ""
		case strings.HasPrefix(line, "="): // somme de contrôle CRC24 de l'armure
		default:
			b64.WriteString(strings.TrimSpace(line))
		}
	}
	if b64.Len() == 0 {
		return nil, fmt.Errorf("clé publique illisible")
	}
	return base64.StdEncoding.DecodeString(b64.String())
}

// verifySums vérifie la signature détachée sig du fichier d'empreintes sums avec la clé embarquée key.
func verifySums(key string, sums, sig []byte) error {
	armored, err := signingKeys.ReadFile("keys/" + key + ".asc")
	if err != nil {
		return fmt.Errorf("clé de signature inconnue : %s", key)
	}
	keyring, err := dearmor(armored)
	if err != nil {
		return err
	}
	gpgv, err := exec.LookPath("gpgv")
	if err != nil {
		return fmt.Errorf("gpgv introuvable : sudo apt install gpgv (vérification de la signature des images)")
	}
	dir, err := os.MkdirTemp("", "labctl-gpgv-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	files := map[string][]byte{"keyring.gpg": keyring, "sums": sums, "sums.sig": sig}
	for name, b := range files {
		if err := os.WriteFile(filepath.Join(dir, name), b, 0o600); err != nil {
			return err
		}
	}
	cmd := exec.Command(gpgv, "--keyring", filepath.Join(dir, "keyring.gpg"), filepath.Join(dir, "sums.sig"), filepath.Join(dir, "sums"))
	cmd.Env = append(os.Environ(), "GNUPGHOME="+dir) // jamais le trousseau de l'utilisateur
	if out, err := cmd.CombinedOutput(); err != nil {
		lines := strings.Split(strings.TrimSpace(string(out)), "\n")
		return fmt.Errorf("signature du fichier d'empreintes invalide (clé %s) : %s", key, lines[len(lines)-1])
	}
	return nil
}
