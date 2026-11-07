package lab

import (
	"bytes"
	"fmt"
	"io"
	"os/exec"
	"regexp"
	"strings"

	"labs/labctl/internal/virt"
)

var reLayout = regexp.MustCompile(`^[a-z]{2,3}(,[a-z]{2,3})*$`)

// keyboardScript règle la disposition dans la VM (en root ; Debian, Ubuntu, Kali...). localectl peut être
// refusé (par l'agent invité) : /etc/default/keyboard fait foi.
func keyboardScript(layout string) string {
	return fmt.Sprintf(`set -e
if command -v localectl >/dev/null; then localectl set-x11-keymap %[1]s 2>/dev/null || true; fi
if [ -f /etc/default/keyboard ]; then
  sed -i 's/^XKBLAYOUT=.*/XKBLAYOUT="%[1]s"/' /etc/default/keyboard
else
  printf 'XKBMODEL="pc105"\nXKBLAYOUT="%[1]s"\nXKBVARIANT=""\nXKBOPTIONS=""\n' > /etc/default/keyboard
fi
command -v setupcon >/dev/null && setupcon --force --save -k >/dev/null 2>&1 || true
grep XKBLAYOUT /etc/default/keyboard`, layout)
}

// VMKeyboard règle le clavier d'une VM démarrée : par SSH (utilisateur des labs) si possible, sinon par
// l'agent invité QEMU (VMs sans SSH par clé, ex. Kali). layout vide : celui des labs (Keyboard).
// L'interface graphique prend le nouveau clavier à la prochaine ouverture de session.
func (l *Lab) VMKeyboard(name, layout string, out io.Writer) error {
	if layout == "" {
		layout = l.Keyboard()
	}
	if !reLayout.MatchString(layout) {
		return fmt.Errorf("disposition invalide : %s", layout)
	}
	v := virt.Shared()
	switch DomState(name) {
	case "absente":
		return fmt.Errorf("%s n'existe pas", name)
	case "running":
	default:
		return fmt.Errorf("%s est arrêtée", name)
	}
	script := keyboardScript(layout)
	var result, how string
	if ip := l.VMIP(name); ip != "" {
		target := l.Setting("LABS_VM_USER", "lab") + "@" + ip
		args := l.sshArgs(target)
		if exec.Command("ssh", append(args, "true")...).Run() == nil {
			how = "SSH"
			cmd := exec.Command("ssh", append(args, "sudo sh -s")...)
			cmd.Stdin = strings.NewReader(script)
			var b bytes.Buffer
			cmd.Stdout, cmd.Stderr = &b, &b
			if err := cmd.Run(); err != nil {
				return fmt.Errorf("%s : échec (SSH) : %s", name, strings.TrimSpace(b.String()))
			}
			result = b.String()
		}
	}
	if how == "" {
		if !v.AgentReady(name) {
			return fmt.Errorf("%s : ni SSH par clé ni agent invité QEMU ; régler le clavier à la main dans la VM (ex. sudo localectl set-x11-keymap %s)", name, layout)
		}
		how = "agent invité"
		o, code, err := v.AgentExec(name, script)
		if err != nil || code != 0 {
			return fmt.Errorf("%s : échec (agent invité) : %v %s", name, err, strings.TrimSpace(o))
		}
		result = o
	}
	line := ""
	for _, s := range strings.Split(result, "\n") {
		if strings.HasPrefix(s, "XKBLAYOUT") {
			line = s
		}
	}
	fmt.Fprintf(out, "ok  %s : clavier %s (%s) — %s\n", name, layout, how, line)
	return nil
}
