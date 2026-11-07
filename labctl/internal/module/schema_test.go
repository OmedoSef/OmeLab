package module

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

var updateSchema = flag.Bool("update", false, "régénère module.schema.json depuis les structures")

// Générateur du schéma : la structure vient des types Go (réflexion, tags yaml), les descriptions des commentaires
// de ces types. Il tourne dans les tests (il a besoin des sources) ; le binaire embarque le résultat.

type schemaGen struct {
	fields map[string]map[string]string // type -> champ Go -> commentaire
	types  map[string]string            // type -> commentaire du type
}

func newSchemaGen(t *testing.T) *schemaGen {
	g := &schemaGen{fields: map[string]map[string]string{}, types: map[string]string{}}
	files, _ := filepath.Glob("*.go")
	fset := token.NewFileSet()
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, parser.ParseComments)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range f.Decls {
			gd, ok := d.(*ast.GenDecl)
			if !ok || gd.Tok != token.TYPE {
				continue
			}
			for _, s := range gd.Specs {
				ts := s.(*ast.TypeSpec)
				st, ok := ts.Type.(*ast.StructType)
				if !ok {
					continue
				}
				doc := ts.Doc
				if doc == nil && len(gd.Specs) == 1 {
					doc = gd.Doc
				}
				if doc != nil {
					g.types[ts.Name.Name] = oneLine(doc.Text())
				}
				m := map[string]string{}
				for _, fl := range st.Fields.List {
					var txt []string
					if fl.Doc != nil {
						txt = append(txt, fl.Doc.Text())
					}
					if fl.Comment != nil {
						txt = append(txt, fl.Comment.Text())
					}
					for _, n := range fl.Names {
						m[n.Name] = oneLine(strings.Join(txt, " "))
					}
				}
				g.fields[ts.Name.Name] = m
			}
		}
	}
	return g
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

func (g *schemaGen) schema(t reflect.Type) map[string]any {
	switch t.Kind() {
	case reflect.Pointer:
		return g.schema(t.Elem())
	case reflect.String: // le lecteur YAML prend aussi un nombre ou un booléen non cité (mode: 0644)
		return map[string]any{"type": []string{"string", "number", "boolean"}}
	case reflect.Int, reflect.Int64:
		return map[string]any{"type": "integer"}
	case reflect.Bool:
		return map[string]any{"type": "boolean"}
	case reflect.Slice: // section vide (run:) acceptée comme par labctl
		return map[string]any{"type": []string{"array", "null"}, "items": g.schema(t.Elem())}
	case reflect.Map:
		return map[string]any{"type": []string{"object", "null"}, "additionalProperties": g.schema(t.Elem())}
	case reflect.Struct:
		props, req := map[string]any{}, []string{}
		g.fields2props(t, props, &req)
		o := map[string]any{"type": []string{"object", "null"}, "additionalProperties": false, "properties": props}
		if len(req) > 0 {
			sort.Strings(req)
			o["required"] = req
		}
		return o
	}
	panic("type non géré par le générateur de schéma : " + t.String())
}

// fields2props ajoute les champs YAML d'une structure (inline compris). Obligatoire = sans omitempty, comme le
// veut la validation, sauf vms (un module peut ne déclarer qu'un réseau).
func (g *schemaGen) fields2props(t reflect.Type, props map[string]any, req *[]string) {
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		name, opts, _ := strings.Cut(f.Tag.Get("yaml"), ",")
		if name == "-" {
			continue
		}
		if strings.Contains(","+opts+",", ",inline,") {
			g.fields2props(f.Type, props, req)
			continue
		}
		if name == "" {
			name = strings.ToLower(f.Name)
		}
		s := g.schema(f.Type)
		desc := g.fields[t.Name()][f.Name]
		if desc == "" { // à défaut, le commentaire du type du champ (ex. Network)
			ft := f.Type
			for ft.Kind() == reflect.Pointer || ft.Kind() == reflect.Slice || ft.Kind() == reflect.Map {
				ft = ft.Elem()
			}
			desc = g.types[ft.Name()]
		}
		if desc != "" {
			s["description"] = desc
		}
		props[name] = s
		if !strings.Contains(opts, "omitempty") && !(t == reflect.TypeOf(Module{}) && name == "vms") {
			*req = append(*req, name)
		}
	}
}

func buildSchema(t *testing.T) []byte {
	g := newSchemaGen(t)
	root := g.schema(reflect.TypeOf(Module{}))
	root["type"] = "object"
	root["$schema"] = "http://json-schema.org/draft-07/schema#"
	root["title"] = "OmeLab — module.yaml"
	root["description"] = "Module déclaratif d'OmeLab (labctl). Référence : docs/module-yaml.md. Généré depuis le code de labctl."
	b, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return append(b, '\n')
}

// Le schéma embarqué doit être exactement celui que produit le code actuel.
func TestSchemaUpToDate(t *testing.T) {
	want := buildSchema(t)
	if *updateSchema {
		if err := os.WriteFile(SchemaFile, want, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	if !bytes.Equal(SchemaJSON, want) {
		t.Fatal("module.schema.json n'est plus à jour : go test ./internal/module -run TestSchemaUpToDate -update")
	}
}

// --- Validation d'un document contre le schéma (le sous-ensemble qu'on produit), pour les tests.

func schemaCheck(s map[string]any, v any, path string, errs *[]string) {
	if !schemaTypeOK(s["type"], v) {
		*errs = append(*errs, fmt.Sprintf("%s : type %T refusé", path, v))
		return
	}
	switch x := v.(type) {
	case map[string]any:
		props, _ := s["properties"].(map[string]any)
		if req, ok := s["required"].([]any); ok {
			for _, r := range req {
				if _, ok := x[r.(string)]; !ok {
					*errs = append(*errs, fmt.Sprintf("%s.%s : obligatoire", path, r))
				}
			}
		}
		for k, val := range x {
			if ps, ok := props[k].(map[string]any); ok {
				schemaCheck(ps, val, path+"."+k, errs)
				continue
			}
			switch ap := s["additionalProperties"].(type) {
			case bool:
				if !ap {
					*errs = append(*errs, fmt.Sprintf("%s.%s : clé inconnue", path, k))
				}
			case map[string]any:
				schemaCheck(ap, val, path+"."+k, errs)
			}
		}
	case []any:
		if it, ok := s["items"].(map[string]any); ok {
			for i, e := range x {
				schemaCheck(it, e, fmt.Sprintf("%s[%d]", path, i), errs)
			}
		}
	}
}

func schemaTypeOK(ty any, v any) bool {
	var types []string
	switch x := ty.(type) {
	case nil:
		return true
	case string:
		types = []string{x}
	case []any:
		for _, e := range x {
			types = append(types, e.(string))
		}
	}
	for _, t := range types {
		switch v.(type) {
		case map[string]any:
			if t == "object" {
				return true
			}
		case []any:
			if t == "array" {
				return true
			}
		case string:
			if t == "string" {
				return true
			}
		case int:
			if t == "integer" || t == "number" {
				return true
			}
		case float64:
			if t == "number" {
				return true
			}
		case bool:
			if t == "boolean" {
				return true
			}
		case nil:
			if t == "null" {
				return true
			}
		}
	}
	return false
}

func checkYAML(t *testing.T, schema map[string]any, doc []byte) []string {
	t.Helper()
	var v any
	if err := yaml.Unmarshal(doc, &v); err != nil {
		t.Fatal(err)
	}
	var errs []string
	schemaCheck(schema, v, "", &errs)
	return errs
}

// Tous les vrais modules du dépôt passent le schéma (pas de fausse erreur dans l'éditeur), et une faute de frappe
// ou un type faux sont bien signalés.
func TestSchemaAcceptsRealModules(t *testing.T) {
	var schema map[string]any
	if err := json.Unmarshal(SchemaJSON, &schema); err != nil {
		t.Fatal(err)
	}
	files, _ := filepath.Glob("../../../modules/*/module.yaml")
	if len(files) == 0 {
		t.Fatal("aucun module trouvé")
	}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if errs := checkYAML(t, schema, b); len(errs) > 0 {
			t.Errorf("%s refusé par le schéma : %v", f, errs)
		}
	}
	for doc, want := range map[string]string{
		"module: m\nnetwork: {subdomian: x}\n":                    "clé inconnue",
		"module: m\nvms:\n  srv: {ip: dix}\n":                     "type",
		"module: m\nvms:\n  srv:\n    files:\n      - {src: a}\n": "obligatoire",
		"description: sans nom\n":                                 "obligatoire",
	} {
		errs := checkYAML(t, schema, []byte(doc))
		if len(errs) == 0 || !strings.Contains(strings.Join(errs, " "), want) {
			t.Errorf("%q : erreur « %s » attendue, obtenu %v", doc, want, errs)
		}
	}
}
