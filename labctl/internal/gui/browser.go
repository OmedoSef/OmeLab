package gui

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Ouverture de l'interface au lancement de labctl gui
type Open int

const (
	OpenBrowser Open = iota // navigateur par défaut (xdg-open)
	OpenApp                 // fenêtre d'application dédiée ; sa fermeture arrête le serveur
	OpenNone
)

// appBrowsers : navigateurs qui savent ouvrir une fenêtre sans barre d'adresse (--app)
var appBrowsers = []string{"chromium", "chromium-browser", "google-chrome", "google-chrome-stable", "microsoft-edge", "brave-browser"}

// startApp ouvre url dans une fenêtre d'application. Un profil à part (dans le cache de l'utilisateur) fait du
// navigateur un processus à lui : il se termine à la fermeture de la fenêtre, ce que signale done.
func startApp(url string) (done <-chan struct{}, err error) {
	bin := ""
	for _, b := range appBrowsers {
		if p, err := exec.LookPath(b); err == nil {
			bin = p
			break
		}
	}
	if bin == "" {
		return nil, fmt.Errorf("aucun navigateur compatible (%s)", strings.Join(appBrowsers, ", "))
	}
	cache, err := os.UserCacheDir()
	if err != nil {
		return nil, err
	}
	profile := filepath.Join(cache, "labctl", "gui-app")
	if err := os.MkdirAll(profile, 0o700); err != nil {
		return nil, err
	}
	cmd := exec.Command(bin, "--app="+url, "--user-data-dir="+profile, "--class=labctl", "--window-size=1320,880",
		"--no-first-run", "--no-default-browser-check")
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	ch := make(chan struct{})
	go func() { cmd.Wait(); close(ch) }()
	return ch, nil
}
