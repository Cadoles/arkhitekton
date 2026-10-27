package ui

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"forge.cadoles.com/vcarroy/arkhitekton/sdk/manifest"
	schema "forge.cadoles.com/vcarroy/arkhitekton/sdk/ui"
)

// references checks what the merged resources name: parents, widgets, navigations, components,
// permissions, and the routes a plugin's blocks call, whose plugin, scope and permission it fills
// in.
func (b *builder) references() []error {
	var problems []error
	add := func(err error) {
		if err != nil {
			problems = append(problems, err)
		}
	}
	paths := map[string]string{}
	for _, s := range b.result.screens {
		where := fmt.Sprintf("Screen %s", s.name)
		add(b.parentProblem(s))
		add(b.permissionProblem(where+": permission", s.spec.Permission))
		if s.owner != schema.Core && s.spec.Data != nil {
			add(b.sources(s.owner, s.spec.Data, where+": data"))
		} else {
			add(coreSources(s.spec.Data, where+": data"))
		}
		key := s.spec.Parent + "\x00" + s.spec.Path
		if other, taken := paths[key]; taken {
			add(fmt.Errorf("%s: the same route as screen %s", where, other))
		}
		paths[key] = s.name
		for _, entry := range s.blocks {
			add(b.blockProblems(entry, fmt.Sprintf("%s: block %s", where, entry.spec.ID)))
		}
	}
	for _, n := range b.result.navigations {
		for _, entry := range n.items {
			add(b.permissionProblem(fmt.Sprintf("Navigation %s: item %s: permission", n.name, entry.spec.ID), entry.spec.Permission))
		}
	}
	for _, w := range b.result.widgets {
		w.spec.Widget.Walk(func(node *schema.Widget) {
			add(b.nodeProblem(w.owner, node, fmt.Sprintf("Widget %s", w.name)))
		})
		add(b.widgetCycle(w.name, nil))
	}
	return problems
}

func (b *builder) parentProblem(s *screen) error {
	seen := map[string]bool{s.name: true}
	for current := s; current.spec.Parent != ""; {
		parent := b.screen(current.spec.Parent)
		if parent == nil {
			return fmt.Errorf("Screen %s: parent %q: no such screen", current.name, current.spec.Parent)
		}
		if seen[parent.name] {
			return fmt.Errorf("Screen %s: its parents loop through %s", s.name, parent.name)
		}
		seen[parent.name] = true
		current = parent
	}
	return nil
}

func (b *builder) permissionProblem(where, permission string) error {
	if permission == "" || b.options.KnownPermission == nil || b.options.KnownPermission(permission) {
		return nil
	}
	return fmt.Errorf("%s: no role grants %q", where, permission)
}

func coreSources(data map[string]schema.DataSource, where string) error {
	for name, source := range data {
		if source.Source != schema.Core {
			return fmt.Errorf("%s.%s: the core reads its own sources", where, name)
		}
	}
	return nil
}

// sources resolves a plugin's data sources to the GET routes it declares.
func (b *builder) sources(plugin string, data map[string]schema.DataSource, where string) error {
	for _, name := range slices.Sorted(maps.Keys(data)) {
		source := data[name]
		if source.Source != "plugin" {
			continue
		}
		route, found := b.route(plugin, "GET", source.Path)
		if !found {
			return fmt.Errorf("%s.%s: GET %s: %s declares no such route", where, name, source.Path, plugin)
		}
		source.Plugin, source.Scope = plugin, route.EffectiveScope()
		data[name] = source
	}
	return nil
}

func (b *builder) blockProblems(entry *block, where string) error {
	spec := &entry.spec
	if entry.owner == schema.Core {
		if err := coreSources(spec.Data, where+": data"); err != nil {
			return err
		}
	} else if err := b.sources(entry.owner, spec.Data, where+": data"); err != nil {
		return err
	}
	if err := b.permissionProblem(where+": permission", spec.Permission); err != nil {
		return err
	}
	if spec.Component != "" {
		if owner := schema.ComponentOwner(spec.Component); owner != schema.Core && owner != entry.owner {
			return fmt.Errorf("%s: component %s belongs to %s", where, spec.Component, owner)
		}
	}
	if spec.Widget != nil {
		var problem error
		spec.Widget.Walk(func(node *schema.Widget) {
			if problem == nil {
				problem = b.nodeProblem(entry.owner, node, where)
			}
		})
		if problem != nil {
			return problem
		}
	}
	for _, name := range slices.Sorted(maps.Keys(entry.slots)) {
		for _, nested := range entry.slots[name] {
			if err := b.blockProblems(nested, fmt.Sprintf("%s: slot %s: block %s", where, name, nested.spec.ID)); err != nil {
				return err
			}
		}
	}
	return nil
}

// nodeProblem checks one widget of an owner's: the composed widget it uses, the navigation its
// tabs show, the route its action calls.
func (b *builder) nodeProblem(owner string, node *schema.Widget, where string) error {
	switch {
	case node.Use != nil:
		used := b.widget(node.Use.Widget)
		if used == nil {
			return fmt.Errorf("%s: widget %q: no such widget", where, node.Use.Widget)
		}
		if used.owner != schema.Core && used.owner != owner {
			return fmt.Errorf("%s: widget %s belongs to %s", where, used.name, used.owner)
		}
		for name, param := range used.spec.Params {
			if _, bound := node.Use.With[name]; param.Required && !bound {
				return fmt.Errorf("%s: widget %s: parameter %s is required", where, used.name, name)
			}
		}
		for name := range node.Use.With {
			if _, declared := used.spec.Params[name]; !declared {
				return fmt.Errorf("%s: widget %s takes no parameter %s", where, used.name, name)
			}
		}
	case node.Tabs != nil:
		if b.navigation(node.Tabs.Navigation) == nil {
			return fmt.Errorf("%s: navigation %q: no such navigation", where, node.Tabs.Navigation)
		}
	case node.Action != nil:
		if owner == schema.Core {
			return fmt.Errorf("%s: the core acts through its components", where)
		}
		route, found := b.route(owner, node.Action.Method, string(node.Action.Path))
		if !found {
			return fmt.Errorf("%s: action %s %s: %s declares no such route", where, node.Action.Method, node.Action.Path, owner)
		}
		node.Action.Plugin, node.Action.Scope, node.Action.Permission = owner, route.EffectiveScope(), route.Permission
	}
	return nil
}

func (b *builder) widgetCycle(name string, through []string) error {
	if slices.Contains(through, name) {
		return fmt.Errorf("Widget %s: uses itself through %s", through[0], strings.Join(append(through, name), ", "))
	}
	used := b.widget(name)
	if used == nil {
		return nil
	}
	var problem error
	used.spec.Widget.Walk(func(node *schema.Widget) {
		if problem == nil && node.Use != nil {
			problem = b.widgetCycle(node.Use.Widget, append(slices.Clone(through), name))
		}
	})
	return problem
}

// route finds the plugin's declared route a templated path calls: a segment holding a template
// stands for a parameter, a literal one must be the route's.
func (b *builder) route(plugin, method, path string) (manifest.Route, bool) {
	segments := strings.Split(path, "/")
	for _, route := range b.routes[plugin] {
		if route.Method == method && matches(strings.Split(route.Path, "/"), segments) {
			return route, true
		}
	}
	return manifest.Route{}, false
}

func matches(route, path []string) bool {
	for i, segment := range route {
		if strings.HasPrefix(segment, "{") && strings.HasSuffix(segment, "...}") {
			return len(path) > i
		}
		if i >= len(path) || path[i] == "" {
			return false
		}
		parameter := strings.HasPrefix(segment, "{")
		if !parameter && path[i] != segment {
			return false
		}
	}
	return len(route) == len(path)
}
