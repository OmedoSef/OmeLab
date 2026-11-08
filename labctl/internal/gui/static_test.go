package gui

import (
	"io/fs"
	"regexp"
	"strings"
	"testing"
)

var (
	reImport = regexp.MustCompile(`(?m)^import (?:\{([^}]*)\} from )?"\./([\w.]+)";$`)
	reExport = regexp.MustCompile(`(?m)^export\s+(?:async\s+)?(?:function\s*\*?\s*([\w$]+)|(?:const|let|var)\s+([\w$]+))`)
	reMore   = regexp.MustCompile(`(?m)^export\s+let\s+[^\n]*`)
)

// Les modules ES de la page tiennent ensemble : chaque nom importé est exporté par le fichier visé (une fonction
// renommée casserait la page sans autre contrôle), util.js n'importe rien (évalué en premier : $, esc, api sont
// toujours prêts), index.html ne charge que main.js, et chaque module est atteint depuis main.js.
func TestStaticModules(t *testing.T) {
	js, _ := fs.Sub(static, "static/js")
	files := map[string]string{}
	_ = fs.WalkDir(js, ".", func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(p, ".js") {
			b, _ := fs.ReadFile(js, p)
			files[p] = string(b)
		}
		return err
	})
	exports := map[string]map[string]bool{}
	for f, s := range files {
		exports[f] = map[string]bool{}
		for _, m := range reExport.FindAllStringSubmatch(s, -1) {
			exports[f][m[1]+m[2]] = true
		}
		for _, line := range reMore.FindAllString(s, -1) { // export let a = 1, b = 2
			for _, part := range strings.Split(line, ",")[1:] {
				if n := regexp.MustCompile(`^\s*([\w$]+)\s*=`).FindStringSubmatch(part); n != nil {
					exports[f][n[1]] = true
				}
			}
		}
	}
	graph := map[string][]string{}
	for f, s := range files {
		for _, m := range reImport.FindAllStringSubmatch(s, -1) {
			target := m[2]
			if _, ok := files[target]; !ok {
				t.Errorf("%s importe %s, absent", f, target)
				continue
			}
			graph[f] = append(graph[f], target)
			for _, n := range strings.Split(m[1], ",") {
				if n = strings.TrimSpace(n); n != "" && !exports[target][n] {
					t.Errorf("%s importe %s depuis %s, qui ne l'exporte pas", f, n, target)
				}
			}
		}
	}
	if len(graph["util.js"]) != 0 {
		t.Errorf("util.js importe %v : il doit rester sans import", graph["util.js"])
	}
	seen := map[string]bool{}
	var visit func(string)
	visit = func(f string) {
		if !seen[f] {
			seen[f] = true
			for _, g := range graph[f] {
				visit(g)
			}
		}
	}
	visit("main.js")
	for f := range files {
		if !seen[f] {
			t.Errorf("%s n'est atteint par aucun import depuis main.js", f)
		}
	}
	html, _ := fs.ReadFile(static, "static/index.html")
	scripts := regexp.MustCompile(`<script[^>]*>`).FindAllString(string(html), -1)
	if len(scripts) != 1 || scripts[0] != `<script type="module" src="js/main.js">` {
		t.Errorf("index.html doit ne charger que js/main.js en module : %q", scripts)
	}
}
