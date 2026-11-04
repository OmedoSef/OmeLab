package virt

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"time"

	"github.com/digitalocean/go-libvirt"
)

// AgentCommand envoie une commande JSON à l'agent invité QEMU d'une VM et renvoie sa réponse.
func (c *Conn) AgentCommand(name, cmd string, timeout time.Duration) (string, error) {
	var out string
	err := c.do(func(lv *libvirt.Libvirt) error {
		d, err := c.lookup(lv, name)
		if err != nil {
			return err
		}
		r, err := lv.QEMUDomainAgentCommand(d, cmd, int32(timeout/time.Second), 0)
		if err != nil {
			return err
		}
		if len(r) > 0 {
			out = r[0]
		}
		return nil
	})
	return out, err
}

// AgentReady indique si l'agent invité d'une VM répond.
func (c *Conn) AgentReady(name string) bool {
	_, err := c.AgentCommand(name, `{"execute":"guest-ping"}`, 5*time.Second)
	return err == nil
}

// AgentExec lance « sh -c script » dans la VM par l'agent invité (en root) et attend sa fin (2 min au plus).
// Renvoie les sorties standard et d'erreur réunies, et le code de retour.
func (c *Conn) AgentExec(name, script string) (string, int, error) {
	req, _ := json.Marshal(map[string]any{"execute": "guest-exec", "arguments": map[string]any{
		"path": "/bin/sh", "arg": []string{"-c", script}, "capture-output": true}})
	resp, err := c.AgentCommand(name, string(req), 10*time.Second)
	if err != nil {
		return "", -1, err
	}
	var started struct {
		Return struct {
			PID int `json:"pid"`
		} `json:"return"`
	}
	if err := json.Unmarshal([]byte(resp), &started); err != nil {
		return "", -1, fmt.Errorf("réponse de l'agent illisible : %s", resp)
	}
	for t := 0; t < 120; t++ {
		resp, err := c.AgentCommand(name, fmt.Sprintf(`{"execute":"guest-exec-status","arguments":{"pid":%d}}`, started.Return.PID), 10*time.Second)
		if err != nil {
			return "", -1, err
		}
		var st struct {
			Return struct {
				Exited   bool   `json:"exited"`
				ExitCode int    `json:"exitcode"`
				Out      string `json:"out-data"`
				Err      string `json:"err-data"`
			} `json:"return"`
		}
		if err := json.Unmarshal([]byte(resp), &st); err != nil {
			return "", -1, err
		}
		if st.Return.Exited {
			o, _ := base64.StdEncoding.DecodeString(st.Return.Out)
			e, _ := base64.StdEncoding.DecodeString(st.Return.Err)
			return string(o) + string(e), st.Return.ExitCode, nil
		}
		time.Sleep(time.Second)
	}
	return "", -1, fmt.Errorf("commande toujours en cours au bout de 2 min")
}
