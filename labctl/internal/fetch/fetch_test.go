package fetch

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// Chaque redirection d'un téléchargement : https seulement, jamais vers Microsoft, dix au plus.
func TestCheckRedirect(t *testing.T) {
	req := func(raw string) *http.Request {
		u, _ := url.Parse(raw)
		return &http.Request{URL: u}
	}
	for _, c := range []struct {
		to, want string
		via      int
	}{
		{"https://mirror.example.org/x.qcow2", "", 1},
		{"http://mirror.example.org/SHA256SUMS", "https attendu", 1},
		{"https://software.download.prss.microsoft.com/x", "Microsoft", 1},
		{"https://mirror.example.org/x", "trop de redirections", 10},
	} {
		err := checkRedirect(req(c.to), make([]*http.Request, c.via))
		if (c.want == "") != (err == nil) || (err != nil && !strings.Contains(err.Error(), c.want)) {
			t.Errorf("%s : %v, attendu %q", c.to, err, c.want)
		}
	}
}
