package module

import (
	"fmt"
	"reflect"
	"strings"
)

// ExpandVars résout les variables ${...} dans TOUS les champs de la config, sauf module:, description: et le
// bloc vars: lui-même (qui définit les variables). Deux familles :
//
//   - globales — ${var.*}, ${module.name|domain}, ${network.subnet|gateway}, ${keyboard} — valables partout ;
//   - par VM — ${vm.name|ip|fqdn} — seulement dans les champs d'une VM (contexte = cette VM). Au niveau module
//     (network:, connections:, dns:...) elles n'ont pas de « VM courante » : on y utilise ${vms.<nom>.*}.
//
// root est le domaine racine des labs (pour ${module.domain}, ${vm.fqdn}) ; keyboard la disposition de l'hôte ;
// labVars le bloc vars: du lab.yaml, déjà résolu — une variable de même nom y surcharge le défaut du module.
// Les champs numériques/structurels (ip, cpus...) ne contiennent pas de chaîne et ne sont pas touchés.
func (m *Module) ExpandVars(root, keyboard string, labVars map[string]string) error {
	// 1. Résoudre d'abord vars: — une variable peut en référencer une autre, ou une variable globale.
	if err := m.resolveVars(root, keyboard, labVars); err != nil {
		return err
	}
	// 2. Champs de niveau module (hors module/description/vars). VM="" : un ${vm.*} y est une erreur claire.
	mod := VarContext{Module: m, Root: root, Keyboard: keyboard, LabVars: labVars}
	for _, f := range []any{m.Network, m.PKI, &m.DNS, &m.Connections, &m.Attach, &m.Defaults} {
		if err := walkStrings(reflect.ValueOf(f), mod.expandKnown); err != nil {
			return err
		}
	}
	// 3. Chaque VM avec son propre contexte, pour résoudre ${vm.*} dans n'importe lequel de ses champs.
	for _, name := range m.VMNames() {
		vm := VarContext{Module: m, VM: name, Root: root, Keyboard: keyboard, LabVars: labVars}
		if err := walkStrings(reflect.ValueOf(m.VMs[name]), vm.expandKnown); err != nil {
			return fmt.Errorf("vm %s : %w", name, err)
		}
	}
	return nil
}

// ResolveLabVars résout les ${...} internes d'un bloc vars: de lab.yaml (une var peut en référencer une autre,
// ou ${keyboard}). Les variables de module/réseau/VM n'ont pas de sens à ce niveau. À appeler une fois, avant
// de passer la map à ExpandVars des modules.
func ResolveLabVars(vars map[string]string, keyboard string) error {
	ctx := VarContext{Module: &Module{Vars: vars}, Keyboard: keyboard}
	return resolveVarsMap(vars, ctx, "lab.yaml")
}

// expandKnown comme Expand, mais tolère un sous-réseau pas encore assigné : ${network.*} et ${vm.ip} y sont
// laissés tels quels (résolus plus tard, au déploiement, quand le /24 est connu). Les vraies erreurs (variable
// inconnue, ${vm.*} hors d'une VM) restent signalées. Permet d'expanser dès le chargement, sous-réseau ou non.
func (c VarContext) expandKnown(s string) (string, error) {
	out, err := c.Expand(s)
	if err != nil && strings.Contains(err.Error(), "encore inconnu") {
		return out, nil
	}
	return out, err
}

// resolveVars remplace les ${...} dans les valeurs du bloc vars: du module (point fixe : une var peut en
// référencer une autre, ou une var de lab). Le contexte est sans VM — une variable ne peut pas dépendre
// d'une ${vm.*}.
func (m *Module) resolveVars(root, keyboard string, labVars map[string]string) error {
	ctx := VarContext{Module: m, Root: root, Keyboard: keyboard, LabVars: labVars}
	return resolveVarsMap(m.Vars, ctx, "vars")
}

// resolveVarsMap résout en place les ${...} des valeurs d'une map de variables (point fixe), avec le contexte
// donné. who nomme la source pour les messages d'erreur (vars, lab.yaml...).
func resolveVarsMap(vars map[string]string, ctx VarContext, who string) error {
	if len(vars) == 0 {
		return nil
	}
	for pass := 0; pass <= len(vars); pass++ {
		changed := false
		for k, v := range vars {
			nv, err := ctx.expandKnown(v)
			if err != nil {
				return fmt.Errorf("%s.%s : %w", who, k, err)
			}
			if nv != v {
				vars[k] = nv
				changed = true
			}
		}
		if !changed {
			break
		}
	}
	// S'il reste un ${var.*} non résolu, c'est un cycle (a référence b qui référence a).
	for k, v := range vars {
		for _, ref := range Vars(v) {
			if strings.HasPrefix(ref, "var.") {
				return fmt.Errorf("%s.%s : référence circulaire (${%s})", who, k, ref)
			}
		}
	}
	return nil
}

// walkStrings applique fn à toutes les chaînes modifiables atteignables depuis v (structs, pointeurs, slices,
// maps), en profondeur. Les champs non exportés sont ignorés (CanSet faux).
func walkStrings(v reflect.Value, fn func(string) (string, error)) error {
	switch v.Kind() {
	case reflect.Pointer, reflect.Interface:
		if !v.IsNil() {
			return walkStrings(v.Elem(), fn)
		}
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			if f := v.Field(i); f.CanSet() {
				if err := walkStrings(f, fn); err != nil {
					return err
				}
			}
		}
	case reflect.Slice, reflect.Array:
		for i := 0; i < v.Len(); i++ {
			if err := walkStrings(v.Index(i), fn); err != nil {
				return err
			}
		}
	case reflect.Map:
		for _, k := range v.MapKeys() {
			ev := v.MapIndex(k)
			if ev.Kind() == reflect.String {
				nv, err := fn(ev.String())
				if err != nil {
					return err
				}
				v.SetMapIndex(k, reflect.ValueOf(nv))
				continue
			}
			cp := reflect.New(ev.Type()).Elem() // les valeurs d'une map ne sont pas adressables : copie modifiable
			cp.Set(ev)
			if err := walkStrings(cp, fn); err != nil {
				return err
			}
			v.SetMapIndex(k, cp)
		}
	case reflect.String:
		if v.CanSet() {
			nv, err := fn(v.String())
			if err != nil {
				return err
			}
			v.SetString(nv)
		}
	}
	return nil
}
