package ui

import (
	"strings"

	schema "forge.cadoles.com/vcarroy/arkhitekton/sdk/ui"
)

// Viewer is who the interface is filtered for, in a project.
type Viewer struct {
	// Active says a plugin is switched on for the project.
	Active func(plugin string) bool
	// Can says the viewer holds a permission in the project.
	Can func(permission string) bool
}

// View is the interface a viewer gets: what an inactive plugin brings and what the viewer may not
// do left out. Hiding is not refusing: the API still decides.
type View struct {
	Screens     []ScreenView                       `json:"screens"`
	Navigations map[string]schema.Navigation       `json:"navigations"`
	Widgets     map[string]schema.WidgetDefinition `json:"widgets"`
}

// ScreenView is a screen and its name.
type ScreenView struct {
	Name string `json:"name"`
	schema.Screen
}

// For filters the interface for a viewer.
func (i *Interface) For(viewer Viewer) View {
	view := View{
		Screens:     []ScreenView{},
		Navigations: map[string]schema.Navigation{},
		Widgets:     map[string]schema.WidgetDefinition{},
	}
	shown := func(owner, permission string) bool {
		return (owner == schema.Core || viewer.Active(owner)) && (permission == "" || viewer.Can(permission))
	}
	kept := map[string]bool{}
	// Parents come first in no particular order: settle until nothing changes.
	for changed := true; changed; {
		changed = false
		for _, s := range i.screens {
			if kept[s.name] || !shown(s.owner, s.spec.Permission) || (s.spec.Parent != "" && !kept[s.spec.Parent]) {
				continue
			}
			kept[s.name], changed = true, true
		}
	}
	for _, s := range i.screens {
		if !kept[s.name] {
			continue
		}
		spec := s.spec
		spec.Blocks = filterBlocks(s.blocks, shown, viewer)
		view.Screens = append(view.Screens, ScreenView{Name: s.name, Screen: spec})
	}
	for _, n := range i.navigations {
		if !shown(n.owner, "") {
			continue
		}
		var spec schema.Navigation
		if n.home != "" && shown(n.homeOwner, "") {
			spec.Home = n.home
		}
		for _, entry := range n.items {
			if shown(entry.owner, entry.spec.Permission) {
				spec.Items = append(spec.Items, entry.spec)
			}
		}
		view.Navigations[n.name] = spec
	}
	for _, w := range i.widgets {
		if shown(w.owner, "") {
			view.Widgets[w.name] = w.spec
		}
	}
	return view
}

func filterBlocks(blocks []*block, shown func(owner, permission string) bool, viewer Viewer) []schema.Block {
	var kept []schema.Block
	for _, entry := range blocks {
		if !shown(entry.owner, entry.spec.Permission) {
			continue
		}
		spec := entry.spec
		if spec.Widget != nil {
			filtered, keep := filterWidget(*spec.Widget, viewer)
			if !keep {
				continue
			}
			spec.Widget = &filtered
		}
		if len(entry.slots) > 0 {
			spec.Slots = map[string][]schema.Block{}
			for name, entries := range entry.slots {
				spec.Slots[name] = filterBlocks(entries, shown, viewer)
				if spec.Slots[name] == nil {
					spec.Slots[name] = []schema.Block{}
				}
			}
		}
		kept = append(kept, spec)
	}
	return kept
}

// allowed says whether the viewer may act. A permission that depends on the row (`deploy:{id}`)
// is the data's to tell, through the widget's condition.
func allowed(action *schema.Action, viewer Viewer) bool {
	return action.Permission == "" || strings.Contains(action.Permission, "{") || viewer.Can(action.Permission)
}

// filterWidget copies the containers of a widget tree without the actions the viewer may not take.
// Leaves are shared: they are not changed.
func filterWidget(w schema.Widget, viewer Viewer) (schema.Widget, bool) {
	children := func(widgets []schema.Widget) []schema.Widget {
		var kept []schema.Widget
		for _, child := range widgets {
			if filtered, keep := filterWidget(child, viewer); keep {
				kept = append(kept, filtered)
			}
		}
		return kept
	}
	switch {
	case w.Action != nil:
		return w, allowed(w.Action, viewer)
	case w.Stack != nil:
		stack := *w.Stack
		stack.Children = children(stack.Children)
		w.Stack = &stack
		return w, len(stack.Children) > 0
	case w.Grid != nil:
		grid := *w.Grid
		grid.Children = children(grid.Children)
		w.Grid = &grid
		return w, len(grid.Children) > 0
	case w.Card != nil:
		card := *w.Card
		card.Children = children(card.Children)
		w.Card = &card
		return w, len(card.Children) > 0
	case w.List != nil:
		list := *w.List
		item, keep := filterWidget(list.Item, viewer)
		list.Item = item
		w.List = &list
		return w, keep
	case w.Table != nil:
		table := *w.Table
		table.Columns = nil
		for _, column := range w.Table.Columns {
			if column.Action == nil || allowed(column.Action, viewer) {
				table.Columns = append(table.Columns, column)
			}
		}
		w.Table = &table
		return w, len(table.Columns) > 0
	}
	return w, true
}
