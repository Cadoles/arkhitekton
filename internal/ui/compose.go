// Package ui composes the dashboard's interface: the core's screens, menus and widgets, described as
// resources (sdk/ui), and what the active installed plugins bring.
//
// A plugin creates resources and entries under its prefix (`<plugin>.`), and contributes to a core
// resource by stating its name: blocks, menu entries and slot entries added, the main menu's home
// set. It never removes, replaces nor moves what another source owns, and a field two sources set
// is a conflict, refused at installation with the name of its first owner, as server-side apply
// does. A plugin's anchors (after, before) name the core's entries or its own: what one plugin
// brings does not depend on another being installed.
package ui

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"forge.cadoles.com/vcarroy/arkhitekton/sdk/manifest"
	schema "forge.cadoles.com/vcarroy/arkhitekton/sdk/ui"
)

// Contribution is what one installed plugin brings: its resources, and the routes its blocks may
// call.
type Contribution struct {
	Plugin    string
	Resources []schema.Resource
	Routes    []manifest.Route
}

// Options are what the composition checks against.
type Options struct {
	// KnownPermission says a permission exists, so that a typo hides nothing from everyone.
	KnownPermission func(string) bool
}

// PluginError is a plugin's contribution refused.
type PluginError struct {
	Plugin string
	Err    error
}

func (e *PluginError) Error() string { return fmt.Sprintf("plugin %s: %v", e.Plugin, e.Err) }

func (e *PluginError) Unwrap() error { return e.Err }

// Interface is the composed interface, before it is filtered for a viewer.
type Interface struct {
	screens     []*screen
	navigations []*navigation
	widgets     []*widget
}

type screen struct {
	name, owner string
	spec        schema.Screen
	blocks      []*block
}

type block struct {
	owner string
	spec  schema.Block
	slots map[string][]*block
}

type navigation struct {
	name, owner string
	home        string
	homeOwner   string
	items       []*item
}

type item struct {
	owner string
	spec  schema.NavItem
}

type widget struct {
	name, owner string
	spec        schema.WidgetDefinition
}

// Compose merges the core's resources and the plugins' contributions. The core's must compose on
// their own: an error there is the dashboard's, and fatal. A plugin whose contribution does not
// compose is left out, and its error returned: the others still are.
func Compose(core []schema.Resource, contributions []Contribution, options Options) (*Interface, []error) {
	if _, err := build(core, nil, options); err != nil {
		return nil, []error{fmt.Errorf("the core's interface: %w", err)}
	}
	contributions = slices.SortedFunc(slices.Values(contributions), func(a, b Contribution) int {
		return cmp.Compare(a.Plugin, b.Plugin)
	})
	var accepted []Contribution
	var refused []error
	for _, contribution := range contributions {
		if _, err := build(core, append(slices.Clone(accepted), contribution), options); err != nil {
			refused = append(refused, &PluginError{Plugin: contribution.Plugin, Err: err})
			continue
		}
		accepted = append(accepted, contribution)
	}
	composed, _ := build(core, accepted, options)
	return composed, refused
}

// Check says whether a plugin's contribution composes with the core's and those of the active
// plugins, the plugin's other versions aside: what an installation or an activation asks.
func Check(core []schema.Resource, active []Contribution, candidate Contribution, options Options) error {
	others := slices.DeleteFunc(slices.Clone(active), func(c Contribution) bool { return c.Plugin == candidate.Plugin })
	if _, err := build(core, others, options); err != nil {
		return fmt.Errorf("the active plugins do not compose: %w", err)
	}
	_, err := build(core, append(others, candidate), options)
	return err
}

type builder struct {
	options Options
	result  *Interface
	// routes are each plugin's declared routes.
	routes map[string][]manifest.Route
}

func build(core []schema.Resource, contributions []Contribution, options Options) (*Interface, error) {
	b := &builder{options: options, result: &Interface{}, routes: map[string][]manifest.Route{}}
	// The composition fills routes into the resources it is given: it works on copies, since the
	// interface served meanwhile shares them.
	core = clones(core)
	contributions = slices.Clone(contributions)
	for i := range contributions {
		contributions[i].Resources = clones(contributions[i].Resources)
	}
	var problems []error
	for _, resource := range core {
		if err := b.create(schema.Core, resource); err != nil {
			problems = append(problems, err)
		}
	}
	for _, contribution := range contributions {
		b.routes[contribution.Plugin] = contribution.Routes
		for _, resource := range contribution.Resources {
			if strings.HasPrefix(resource.Name, contribution.Plugin+".") {
				if err := b.create(contribution.Plugin, resource); err != nil {
					problems = append(problems, err)
				}
			}
		}
	}
	for _, contribution := range contributions {
		for _, resource := range contribution.Resources {
			if !strings.HasPrefix(resource.Name, contribution.Plugin+".") {
				if err := b.contribute(contribution.Plugin, resource); err != nil {
					problems = append(problems, err)
				}
			}
		}
	}
	if len(problems) == 0 {
		problems = b.references()
	}
	if len(problems) > 0 {
		return nil, errors.Join(problems...)
	}
	return b.result, nil
}

func clones(resources []schema.Resource) []schema.Resource {
	copied := make([]schema.Resource, 0, len(resources))
	for _, resource := range resources {
		raw, err := json.Marshal(resource)
		if err == nil {
			resource, err = schema.Decode(raw)
		}
		if err != nil {
			panic(fmt.Sprintf("copying %s %s: %v", resource.Kind, resource.Name, err))
		}
		copied = append(copied, resource)
	}
	return copied
}

func at(resource schema.Resource) string { return fmt.Sprintf("%s %s", resource.Kind, resource.Name) }

// prefix is what an owner's entries begin with.
func prefix(owner string) string { return owner + "." }

func (b *builder) create(owner string, resource schema.Resource) error {
	where := at(resource)
	if b.exists(resource.Kind, resource.Name) {
		return fmt.Errorf("%s: already defined", where)
	}
	switch {
	case resource.Screen != nil:
		spec := *resource.Screen
		spec.Blocks = nil
		created := &screen{name: resource.Name, owner: owner, spec: spec}
		for i, entry := range resource.Screen.Blocks {
			entryAt := fmt.Sprintf("%s: blocks[%d]", where, i)
			if err := checkCreated(owner, entry, entryAt); err != nil {
				return err
			}
			placed, err := insert(created.blocks, newBlock(owner, entry), func(b *block) string { return b.spec.ID }, entry.After, entry.Before, owner)
			if err != nil {
				return fmt.Errorf("%s: %w", entryAt, err)
			}
			created.blocks = placed
		}
		b.result.screens = append(b.result.screens, created)
	case resource.Navigation != nil:
		created := &navigation{name: resource.Name, owner: owner}
		if resource.Navigation.Home != "" {
			created.home, created.homeOwner = resource.Navigation.Home, owner
		}
		for i, entry := range resource.Navigation.Items {
			if !strings.HasPrefix(entry.ID, prefix(owner)) {
				return fmt.Errorf("%s: items[%d]: %q, an entry of %s begins with %q", where, i, entry.ID, owner, prefix(owner))
			}
			placed, err := insert(created.items, &item{owner: owner, spec: entry}, func(i *item) string { return i.spec.ID }, entry.After, entry.Before, owner)
			if err != nil {
				return fmt.Errorf("%s: items[%d]: %w", where, i, err)
			}
			created.items = placed
		}
		b.result.navigations = append(b.result.navigations, created)
	case resource.Widget != nil:
		b.result.widgets = append(b.result.widgets, &widget{name: resource.Name, owner: owner, spec: *resource.Widget})
	}
	return nil
}

// checkCreated refuses an entry an owner creates without its prefix, or without content, slot
// entries included.
func checkCreated(owner string, entry schema.Block, where string) error {
	if !strings.HasPrefix(entry.ID, prefix(owner)) {
		return fmt.Errorf("%s: %q, a block of %s begins with %q", where, entry.ID, owner, prefix(owner))
	}
	if entry.Contribution() {
		return fmt.Errorf("%s: %q adds to no block: a new block states a component or a widget", where, entry.ID)
	}
	for name, entries := range entry.Slots {
		for i, nested := range entries {
			if err := checkCreated(owner, nested, fmt.Sprintf("%s.slots.%s[%d]", where, name, i)); err != nil {
				return err
			}
		}
	}
	return nil
}

func newBlock(owner string, spec schema.Block) *block {
	created := &block{owner: owner, spec: spec, slots: map[string][]*block{}}
	created.spec.Slots = nil
	for name, entries := range spec.Slots {
		created.slots[name] = nil
		for _, entry := range entries {
			created.slots[name] = append(created.slots[name], newBlock(owner, entry))
		}
	}
	return created
}

func (b *builder) exists(kind schema.Kind, name string) bool {
	switch kind {
	case schema.KindScreen:
		return b.screen(name) != nil
	case schema.KindNavigation:
		return b.navigation(name) != nil
	default:
		return b.widget(name) != nil
	}
}

func (b *builder) screen(name string) *screen {
	for _, candidate := range b.result.screens {
		if candidate.name == name {
			return candidate
		}
	}
	return nil
}

func (b *builder) navigation(name string) *navigation {
	for _, candidate := range b.result.navigations {
		if candidate.name == name {
			return candidate
		}
	}
	return nil
}

func (b *builder) widget(name string) *widget {
	for _, candidate := range b.result.widgets {
		if candidate.name == name {
			return candidate
		}
	}
	return nil
}

// contribute merges a plugin's resource into the core's of the same name.
func (b *builder) contribute(plugin string, resource schema.Resource) error {
	where := at(resource)
	switch {
	case resource.Screen != nil:
		target := b.screen(resource.Name)
		if target == nil || target.owner != schema.Core {
			return fmt.Errorf("%s: no core screen of that name to add to, and a screen %s creates begins with %q", where, plugin, prefix(plugin))
		}
		spec := *resource.Screen
		spec.Blocks = nil
		if field := setField(spec); field != "" {
			return fmt.Errorf("%s: %s is owned by %s", where, field, target.owner)
		}
		merged, err := mergeBlocks(plugin, target.blocks, resource.Screen.Blocks, where+": blocks")
		if err != nil {
			return err
		}
		target.blocks = merged
	case resource.Navigation != nil:
		target := b.navigation(resource.Name)
		if target == nil || target.owner != schema.Core {
			return fmt.Errorf("%s: no core navigation of that name to add to, and a navigation %s creates begins with %q", where, plugin, prefix(plugin))
		}
		if home := resource.Navigation.Home; home != "" {
			if target.homeOwner != "" {
				return fmt.Errorf("%s: home is owned by %s", where, target.homeOwner)
			}
			target.home, target.homeOwner = home, plugin
		}
		for i, entry := range resource.Navigation.Items {
			if !strings.HasPrefix(entry.ID, prefix(plugin)) {
				return fmt.Errorf("%s: items[%d]: %q is owned by %s, or begins with %q to be added", where, i, entry.ID, ownerOf(target.items, entry.ID), prefix(plugin))
			}
			placed, err := insert(target.items, &item{owner: plugin, spec: entry}, func(i *item) string { return i.spec.ID }, entry.After, entry.Before, plugin)
			if err != nil {
				return fmt.Errorf("%s: items[%d]: %w", where, i, err)
			}
			target.items = placed
		}
	case resource.Widget != nil:
		return fmt.Errorf("%s: a widget is not added to, and one %s creates begins with %q", where, plugin, prefix(plugin))
	}
	return nil
}

func ownerOf(items []*item, id string) string {
	for _, candidate := range items {
		if candidate.spec.ID == id {
			return candidate.owner
		}
	}
	return "no one"
}

// setField names a screen field a contribution sets: only blocks may be added to another's
// screen.
func setField(spec schema.Screen) string {
	switch {
	case spec.Path != "":
		return "path"
	case spec.Parent != "":
		return "parent"
	case spec.Redirect != "":
		return "redirect"
	case spec.Permission != "":
		return "permission"
	case spec.Data != nil:
		return "data"
	}
	return ""
}

// mergeBlocks adds a plugin's blocks to a list, or its slot entries to the blocks it names.
func mergeBlocks(plugin string, list []*block, entries []schema.Block, where string) ([]*block, error) {
	for i, entry := range entries {
		entryAt := fmt.Sprintf("%s[%d]", where, i)
		index := slices.IndexFunc(list, func(b *block) bool { return b.spec.ID == entry.ID })
		if index < 0 {
			if err := checkCreated(plugin, entry, entryAt); err != nil {
				return nil, err
			}
			placed, err := insert(list, newBlock(plugin, entry), func(b *block) string { return b.spec.ID }, entry.After, entry.Before, plugin)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", entryAt, err)
			}
			list = placed
			continue
		}
		target := list[index]
		if !entry.Contribution() || entry.After != "" || entry.Before != "" {
			return nil, fmt.Errorf("%s: %q is owned by %s: only its slots may be added to", entryAt, entry.ID, target.owner)
		}
		for name, slotEntries := range entry.Slots {
			current, declared := target.slots[name]
			if !declared {
				return nil, fmt.Errorf("%s: %q has no slot %q", entryAt, entry.ID, name)
			}
			merged, err := mergeBlocks(plugin, current, slotEntries, fmt.Sprintf("%s.slots.%s", entryAt, name))
			if err != nil {
				return nil, err
			}
			target.slots[name] = merged
		}
	}
	return list, nil
}

// insert places an entry after or before an anchor, or last. A plugin's anchor is the core's or
// its own: two plugins never depend on one another's order.
func insert[T any](list []T, entry T, id func(T) string, after, before, owner string) ([]T, error) {
	anchor := cmp.Or(after, before)
	if anchor == "" {
		return append(list, entry), nil
	}
	if owner != schema.Core && !strings.HasPrefix(anchor, prefix(schema.Core)) && !strings.HasPrefix(anchor, prefix(owner)) {
		return nil, fmt.Errorf("anchor %q: a plugin places its entries next to the core's or its own", anchor)
	}
	index := slices.IndexFunc(list, func(candidate T) bool { return id(candidate) == anchor })
	if index < 0 {
		return nil, fmt.Errorf("anchor %q: no such entry", anchor)
	}
	if after != "" {
		index++
	}
	return slices.Insert(list, index, entry), nil
}
