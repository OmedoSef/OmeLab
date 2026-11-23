package lab

import (
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"labs/labctl/internal/virt"
)

// Le lab entier d'un coup (labctl up | start | stop) : la passerelle et tous les modules, ou seulement ceux
// nommés. L'échec d'un module n'arrête pas les autres ; les erreurs sont rendues ensemble à la fin.

// ModuleOrder renvoie les modules (tous, ou only) dans l'ordre où les construire et les démarrer : un module qui
// fournit des VMs branchées ailleurs (attach:, câblage local) passe avant les modules qui les accueillent, puis
// l'ordre des noms. L'arrêt se fait dans l'ordre inverse.
func (l *Lab) ModuleOrder(only []string) ([]string, error) {
	mods, err := l.Modules()
	if err != nil {
		return nil, err
	}
	deps := map[string][]string{} // module -> modules dont il accueille des VMs
	var all []string
	for _, m := range mods {
		if m.YAML == nil {
			continue
		}
		all = append(all, m.Name)
		for _, ref := range m.Conf.Attached {
			if owner, _, ok := strings.Cut(ref, "/"); ok && owner != m.Name {
				deps[m.Name] = append(deps[m.Name], owner)
			}
		}
	}
	slices.Sort(all)
	for _, n := range only {
		if !slices.Contains(all, n) {
			return nil, fmt.Errorf("module inconnu : %s (%s)", n, strings.Join(all, ", "))
		}
	}
	var order []string
	state := map[string]int{} // 1 : en cours, 2 : placé
	var visit func(string)
	visit = func(n string) {
		if state[n] != 0 { // déjà placé, ou cycle (A branché sur B et B sur A) : l'ordre des noms tranche
			return
		}
		state[n] = 1
		ds := slices.Clone(deps[n])
		slices.Sort(ds)
		for _, d := range ds {
			if slices.Contains(all, d) {
				visit(d)
			}
		}
		state[n] = 2
		order = append(order, n)
	}
	for _, n := range all {
		visit(n)
	}
	if len(only) == 0 {
		return order, nil
	}
	return slices.DeleteFunc(order, func(n string) bool { return !slices.Contains(only, n) }), nil
}

// UpAll construit le lab : la passerelle si elle manque (démarrée sinon), puis chaque module (labctl <module> up).
func (l *Lab) UpAll(only []string, o UpOptions) error {
	out := o.Out
	if out == nil {
		out = io.Discard
	}
	order, err := l.ModuleOrder(only)
	if err != nil {
		return err
	}
	g := l.Gateway(out)
	if l.DomState(GatewayVM) == virt.StateAbsent {
		fmt.Fprintf(out, "==> Passerelle %s\n", GatewayVM)
		if err := g.Create(); err != nil {
			return fmt.Errorf("passerelle : %w (les modules n'auraient ni DHCP, ni DNS, ni Internet)", err)
		}
	} else if err := g.Start(); err != nil {
		return fmt.Errorf("passerelle : %w", err)
	}
	var errs []error
	for _, m := range order {
		fmt.Fprintf(out, "==> Module %s\n", m)
		if err := l.ModuleUp(m, nil, o); err != nil {
			fmt.Fprintf(out, "ERREUR %s : %v\n", m, err)
			errs = append(errs, fmt.Errorf("%s : %w", m, err))
		}
	}
	return errors.Join(errs...)
}

// StartAll démarre la passerelle puis les VMs des modules ; un module jamais construit est signalé (labctl up).
func (l *Lab) StartAll(only []string, out io.Writer) error {
	if out == nil {
		out = io.Discard
	}
	order, err := l.ModuleOrder(only)
	if err != nil {
		return err
	}
	if l.DomState(GatewayVM) == virt.StateAbsent {
		fmt.Fprintf(out, "attention: pas de passerelle %s : ni DHCP, ni DNS, ni Internet (labctl up la crée)\n", GatewayVM)
	} else if err := l.Gateway(out).Start(); err != nil {
		return fmt.Errorf("passerelle : %w", err)
	}
	var errs []error
	for _, m := range order {
		if !l.built(m) {
			fmt.Fprintf(out, "attention: %s n'est pas construit : labctl up %s\n", m, m)
			continue
		}
		fmt.Fprintf(out, "==> Module %s\n", m)
		if err := l.ModuleStart(m, nil, out); err != nil {
			errs = append(errs, fmt.Errorf("%s : %w", m, err))
		}
	}
	return errors.Join(errs...)
}

// StopAll arrête les VMs des modules, dans l'ordre inverse du démarrage, puis la passerelle si tout le lab est
// arrêté (sans liste de modules) : les autres modules en ont encore besoin sinon.
func (l *Lab) StopAll(only []string, force bool, out io.Writer) error {
	if out == nil {
		out = io.Discard
	}
	order, err := l.ModuleOrder(only)
	if err != nil {
		return err
	}
	slices.Reverse(order)
	var errs []error
	for _, m := range order {
		if !l.built(m) {
			continue
		}
		fmt.Fprintf(out, "==> Module %s\n", m)
		if err := l.ModuleStop(m, nil, force, out); err != nil {
			errs = append(errs, fmt.Errorf("%s : %w", m, err))
		}
	}
	if len(only) == 0 && l.DomState(GatewayVM) != virt.StateAbsent {
		if err := l.Gateway(out).Stop(); err != nil {
			errs = append(errs, fmt.Errorf("passerelle : %w", err))
		}
	}
	return errors.Join(errs...)
}

// built indique si un module a été construit (son réseau existe).
func (l *Lab) built(name string) bool {
	m, err := l.Module(name)
	return err == nil && m.YAML != nil && l.Hypervisor().NetExists(m.YAML.NetworkName())
}
