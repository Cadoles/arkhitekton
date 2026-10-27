package ui

import (
	"errors"
	"fmt"
	"strings"

	"forge.cadoles.com/vcarroy/arkhitekton/sdk/declare"
)

// Validate checks the resource's structure, each problem named by its path. Whether it may create
// or contribute, and whether what it refers to exists, is the composition's to say.
func (r Resource) Validate() error {
	at := fmt.Sprintf("%s %s", r.Kind, r.Name)
	if !ValidName(r.Name) {
		return fmt.Errorf("%s: %q is not a resource's name", at, r.Name)
	}
	var problems []error
	switch {
	case r.Screen != nil:
		problems = r.Screen.problems(at)
	case r.Navigation != nil:
		problems = r.Navigation.problems(at)
	case r.Widget != nil:
		problems = r.Widget.problems(at)
	default:
		return fmt.Errorf("%s: no spec", at)
	}
	return errors.Join(problems...)
}

func (s Screen) problems(at string) []error {
	var problems []error
	if strings.HasPrefix(s.Path, "/") && s.Parent != "" {
		problems = append(problems, fmt.Errorf("%s.path: relative to the parent's", at))
	}
	if s.Parent != "" && !ValidName(s.Parent) {
		problems = append(problems, fmt.Errorf("%s.parent: %q is not a screen's name", at, s.Parent))
	}
	if s.Redirect != "" && !strings.HasPrefix(s.Redirect, "/") {
		problems = append(problems, fmt.Errorf("%s.redirect: an absolute path", at))
	}
	if s.Redirect != "" && len(s.Blocks) > 0 {
		problems = append(problems, fmt.Errorf("%s: a redirect shows nothing", at))
	}
	problems = append(problems, dataProblems(at+".data", s.Data)...)
	problems = append(problems, blocksProblems(at+".blocks", s.Blocks)...)
	return problems
}

func blocksProblems(at string, blocks []Block) []error {
	var problems []error
	seen := map[string]bool{}
	for i, block := range blocks {
		blockAt := fmt.Sprintf("%s[%d]", at, i)
		if seen[block.ID] {
			problems = append(problems, fmt.Errorf("%s.id: %q twice", blockAt, block.ID))
		}
		seen[block.ID] = true
		problems = append(problems, block.problems(blockAt)...)
	}
	return problems
}

func (b Block) problems(at string) []error {
	var problems []error
	if !ValidName(b.ID) {
		problems = append(problems, fmt.Errorf("%s.id: %q is not a block's id", at, b.ID))
	}
	problems = append(problems, placementProblems(at, b.After, b.Before)...)
	switch {
	case b.Component != "" && b.Widget != nil:
		problems = append(problems, fmt.Errorf("%s: a component or a widget, not both", at))
	case b.Contribution():
		if b.Props != nil || b.Data != nil || b.When != nil || b.Permission != "" {
			problems = append(problems, fmt.Errorf("%s: a block with neither component nor widget only fills slots", at))
		}
	case b.Component != "" && !componentPattern.MatchString(b.Component):
		problems = append(problems, fmt.Errorf("%s.component: %q is not `<owner>/<name>`", at, b.Component))
	case b.Widget != nil:
		problems = append(problems, b.Widget.problems(at+".widget")...)
	}
	if b.Props != nil && b.Component == "" {
		problems = append(problems, fmt.Errorf("%s.props: a component's", at))
	}
	problems = append(problems, dataProblems(at+".data", b.Data)...)
	problems = append(problems, conditionProblems(at+".when", b.When)...)
	for name, entries := range b.Slots {
		if !pathPattern.MatchString(name) {
			problems = append(problems, fmt.Errorf("%s.slots: %q is not a slot's name", at, name))
		}
		problems = append(problems, blocksProblems(at+".slots."+name, entries)...)
	}
	return problems
}

func placementProblems(at, after, before string) []error {
	switch {
	case after != "" && before != "":
		return []error{fmt.Errorf("%s: after or before, not both", at)}
	case after != "" && !ValidName(after):
		return []error{fmt.Errorf("%s.after: %q is not an id", at, after)}
	case before != "" && !ValidName(before):
		return []error{fmt.Errorf("%s.before: %q is not an id", at, before)}
	}
	return nil
}

func dataProblems(at string, data map[string]DataSource) []error {
	var problems []error
	for name, source := range data {
		sourceAt := at + "." + name
		if source.Plugin != "" || source.Scope != "" {
			problems = append(problems, fmt.Errorf("%s: plugin and scope are the route's, not the screen's to state", sourceAt))
		}
		if !pathPattern.MatchString(name) || strings.Contains(name, ".") || name == "route" {
			problems = append(problems, fmt.Errorf("%s: %q is not a free data name", sourceAt, name))
		}
		switch source.Source {
		case Core:
			parameters, known := CoreSources[source.Name]
			if !known {
				problems = append(problems, fmt.Errorf("%s.name: no core source %q", sourceAt, source.Name))
				continue
			}
			if source.Path != "" {
				problems = append(problems, fmt.Errorf("%s.path: a plugin source's", sourceAt))
			}
			for _, parameter := range parameters {
				problems = append(problems, requiredTemplate(sourceAt+".params."+parameter, Template(source.Params[parameter]))...)
			}
			if len(source.Params) > len(parameters) {
				problems = append(problems, fmt.Errorf("%s.params: %s takes %v", sourceAt, source.Name, parameters))
			}
		case "plugin":
			if source.Name != "" || source.Params != nil {
				problems = append(problems, fmt.Errorf("%s: a plugin source states its path only", sourceAt))
			}
			if strings.HasPrefix(source.Path, "/") {
				problems = append(problems, fmt.Errorf("%s.path: relative to the plugin's routes", sourceAt))
			}
			problems = append(problems, requiredTemplate(sourceAt+".path", Template(source.Path))...)
		default:
			problems = append(problems, fmt.Errorf("%s.source: %q, expected core or plugin", sourceAt, source.Source))
		}
	}
	return problems
}

func (n Navigation) problems(at string) []error {
	var problems []error
	if n.Home != "" && !strings.HasPrefix(n.Home, "/") {
		problems = append(problems, fmt.Errorf("%s.home: an absolute path", at))
	}
	seen := map[string]bool{}
	for i, item := range n.Items {
		itemAt := fmt.Sprintf("%s.items[%d]", at, i)
		if !ValidName(item.ID) || seen[item.ID] {
			problems = append(problems, fmt.Errorf("%s.id: %q is not a free id", itemAt, item.ID))
		}
		seen[item.ID] = true
		problems = append(problems, placementProblems(itemAt, item.After, item.Before)...)
		if item.Label == "" {
			problems = append(problems, fmt.Errorf("%s.label: missing", itemAt))
		}
		if !strings.HasPrefix(item.To, "/") {
			problems = append(problems, fmt.Errorf("%s.to: an absolute path", itemAt))
		}
		problems = append(problems, templateProblems(itemAt+".to", Template(item.To))...)
	}
	return problems
}

func (d WidgetDefinition) problems(at string) []error {
	var problems []error
	for name := range d.Params {
		if !pathPattern.MatchString(name) || strings.Contains(name, ".") {
			problems = append(problems, fmt.Errorf("%s.params: %q is not a parameter's name", at, name))
		}
	}
	problems = append(problems, d.Widget.problems(at+".widget")...)
	if len(d.Widget.Actions()) > 0 {
		problems = append(problems, fmt.Errorf("%s.widget: a composed widget has no action, the block that uses it does", at))
	}
	return problems
}

func templateProblems(at string, template Template) []error {
	if template == "" {
		return nil
	}
	if _, err := declare.ParseDisplayTemplate(string(template)); err != nil {
		return []error{fmt.Errorf("%s: %w", at, err)}
	}
	return nil
}
