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
		{"https://download.microsoft.com./x/dev.zip", "Microsoft", 1}, // point final : même hôte
		{"https://DOWNLOAD.MICROSOFT.COM../x", "Microsoft", 1},
		{"https://aka.ms/win11", "Microsoft", 1},
		{"https://cdn.example.org/Win11_24H2.iso", "jamais d'ISO", 1}, // une adresse anodine ne mène pas à un ISO
	} {
		via := make([]*http.Request, c.via)
		via[0] = req("https://mirror.example.org/x.zip")
		err := checkRedirect(req(c.to), via)
		if (c.want == "") != (err == nil) || (err != nil && !strings.Contains(err.Error(), c.want)) {
			t.Errorf("%s : %v, attendu %q", c.to, err, c.want)
		}
	}
}

// Un ISO voulu (pilotes virtio-win, empreinte figée) peut passer par un miroir.
func TestCheckRedirectWantedISO(t *testing.T) {
	from, _ := url.Parse("https://fedorapeople.org/virtio-win-0.1.302.iso")
	to, _ := url.Parse("https://mirror.example.org/virtio-win-0.1.302.iso")
	if err := checkRedirect(&http.Request{URL: to}, []*http.Request{{URL: from}}); err != nil {
		t.Errorf("ISO voulu refusé : %v", err)
	}
}
