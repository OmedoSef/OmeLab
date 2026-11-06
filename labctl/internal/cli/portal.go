package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"labs/labctl/internal/coreconf"
	"labs/labctl/internal/portal"
)

// gwPortalCmd : service de portail captif, lancé sur la passerelle (gwportal.service). Caché : il n'a de sens
// que sur core-gw, où labctl est copié par « labctl core update ». Il fonctionne sans dossier Labs.
func gwPortalCmd() *cobra.Command {
	c := &cobra.Command{Use: "gw-portal", Short: "Portails captifs (service de la passerelle core-gw)", Hidden: true}
	var conf, state string
	serve := &cobra.Command{Use: "serve", Short: "Sert les portails de " + portal.ConfigPath, Args: cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			confs, err := portal.LoadConfig(conf)
			if err != nil {
				return err
			}
			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			fmt.Printf("%d portail(s)\n", len(confs))
			return portal.New(confs, state).Serve(ctx)
		}}
	serve.Flags().StringVar(&conf, "config", portal.ConfigPath, "configuration des portails")
	serve.Flags().StringVar(&state, "state", portal.StatePath, "sessions (gardées entre deux redémarrages du service)")

	control := func(method, path string) ([]byte, error) {
		req, _ := http.NewRequest(method, fmt.Sprintf("http://127.0.0.1:%d%s", coreconf.PortalControlPort, path), nil)
		resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
		if err != nil {
			return nil, fmt.Errorf("service de portail injoignable : %w", err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("%s", b)
		}
		return b, nil
	}
	var asJSON bool
	sessions := &cobra.Command{Use: "sessions", Short: "Sessions en cours", Args: cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			b, err := control("GET", "/sessions")
			if err != nil {
				return err
			}
			if asJSON {
				_, err = os.Stdout.Write(b)
				return err
			}
			var list []portal.Session
			if err := json.Unmarshal(b, &list); err != nil {
				return err
			}
			printSessions(list)
			return nil
		}}
	sessions.Flags().BoolVar(&asJSON, "json", false, "sortie JSON")
	kick := &cobra.Command{Use: "kick PORTAIL IP", Short: "Ferme une session", Args: cobra.ExactArgs(2),
		RunE: func(_ *cobra.Command, a []string) error {
			_, err := control("POST", "/kick?portal="+a[0]+"&ip="+a[1])
			return err
		}}
	c.AddCommand(serve, sessions, kick)
	return c
}

func printSessions(list []portal.Session) {
	if len(list) == 0 {
		fmt.Println("aucune session de portail")
		return
	}
	fmt.Printf("%-8s %-16s %-10s %-20s %s\n", "PORTAIL", "SOURCE", "MODULE", "CIBLE", "EXPIRE")
	for _, s := range list {
		fmt.Printf("%-8d %-16s %-10s %-20s %s (reste %s)\n", s.Portal, s.IP, s.Module, s.Target,
			s.Expires.Local().Format("15:04:05"), time.Until(s.Expires).Round(time.Second))
	}
}
