package ui

import (
	"encoding/json"
	"errors"
	"fmt"
)

/*
 * The widgets: a closed vocabulary, which only the core extends.
 *
 * A widget reads the data its block loaded through display templates (sdk/declare): `{path |
 * filter}`. Inside a table or a list, a row's own fields are read first, then the block's data.
 * A need the vocabulary does not cover is a component the plugin registers, not a new widget here.
 */

// Template is a display template, rendered by the browser.
type Template string

// Widget is exactly one of its fields, and may be shown on a condition.
type Widget struct {
	When []Condition `json:"when,omitempty"`

	Stack    *Stack    `json:"stack,omitempty"`
	Grid     *Grid     `json:"grid,omitempty"`
	Card     *Card     `json:"card,omitempty"`
	Heading  *Heading  `json:"heading,omitempty"`
	KeyValue *KeyValue `json:"keyValue,omitempty"`
	Table    *Table    `json:"table,omitempty"`
	List     *List     `json:"list,omitempty"`
	Badge    *Badge    `json:"badge,omitempty"`
	Stat     *Stat     `json:"stat,omitempty"`
	Text     *Text     `json:"text,omitempty"`
	Markdown *Markdown `json:"markdown,omitempty"`
	Link     *Link     `json:"link,omitempty"`
	Action   *Action   `json:"action,omitempty"`
	Empty    *Empty    `json:"empty,omitempty"`
	Use      *Use      `json:"use,omitempty"`
	Tabs     *Tabs     `json:"tabs,omitempty"`
	Outlet   *Outlet   `json:"outlet,omitempty"`
}

// Stack lays its children out in a column, or a row.
type Stack struct {
	Row      bool     `json:"row,omitempty"`
	Children []Widget `json:"children"`
}

// Grid lays its children out in columns, one on a narrow screen.
type Grid struct {
	Columns  int      `json:"columns"`
	Children []Widget `json:"children"`
}

// Card frames its children under a title.
type Card struct {
	Title       Template `json:"title,omitempty"`
	Collapsible bool     `json:"collapsible,omitempty"`
	Collapsed   bool     `json:"collapsed,omitempty"`
	Children    []Widget `json:"children"`
}

// Heading is a screen's or a section's title.
type Heading struct {
	Text  Template `json:"text"`
	Level int      `json:"level,omitempty"`
}

// KeyValue lists labelled values.
type KeyValue struct {
	Items []KeyValueItem `json:"items"`
}

// KeyValueItem is one labelled value.
type KeyValueItem struct {
	Label string   `json:"label"`
	Value Template `json:"value"`
	Link  Template `json:"link,omitempty"`
	Mono  bool     `json:"mono,omitempty"`
}

// Table shows a list of the data as rows.
type Table struct {
	// Rows is the data path of the list.
	Rows    string   `json:"rows"`
	Empty   string   `json:"empty,omitempty"`
	Columns []Column `json:"columns"`
}

// Column is a value, a badge or an action.
type Column struct {
	Label string   `json:"label"`
	Value Template `json:"value,omitempty"`
	Link  Template `json:"link,omitempty"`
	Mono  bool     `json:"mono,omitempty"`
	Badge *Badge   `json:"badge,omitempty"`
	// Action is the row's, its condition read in the row.
	Action *Action     `json:"action,omitempty"`
	When   []Condition `json:"when,omitempty"`
}

// List renders a widget for each element of a list of the data.
type List struct {
	Items string `json:"items"`
	Empty string `json:"empty,omitempty"`
	Item  Widget `json:"item"`
}

// Badge is a short state, toned.
type Badge struct {
	Text Template `json:"text"`
	Tone Tone     `json:"tone,omitzero"`
}

// Tones a badge or a stat may take.
var Tones = []string{"neutral", "info", "success", "warning", "danger"}

// Tone is fixed (`tone: success`), or read from the data through a table.
type Tone struct {
	Fixed   string            `json:"-"`
	From    string            `json:"from,omitempty"`
	Map     map[string]string `json:"map,omitempty"`
	Default string            `json:"default,omitempty"`
}

type toneObject Tone

func (t *Tone) UnmarshalJSON(raw []byte) error {
	var fixed string
	if json.Unmarshal(raw, &fixed) == nil {
		*t = Tone{Fixed: fixed}
		return nil
	}
	var object toneObject
	if err := strictDecode(raw, &object); err != nil {
		return err
	}
	*t = Tone(object)
	return nil
}

func (t Tone) MarshalJSON() ([]byte, error) {
	if t.Fixed != "" {
		return json.Marshal(t.Fixed)
	}
	return json.Marshal(toneObject(t))
}

func (t Tone) IsZero() bool { return t.Fixed == "" && t.From == "" }

// Stat is a figure, labelled.
type Stat struct {
	Label string   `json:"label"`
	Value Template `json:"value"`
	Hint  Template `json:"hint,omitempty"`
	Tone  Tone     `json:"tone,omitzero"`
}

// Text is a line of text.
type Text struct {
	Value Template `json:"value"`
	Muted bool     `json:"muted,omitempty"`
	Mono  bool     `json:"mono,omitempty"`
}

// Markdown is text with emphasis, code, lists and links; no HTML.
type Markdown struct {
	Value Template `json:"value"`
}

// Link leads to a screen of the dashboard (an absolute path) or to an https address.
type Link struct {
	Label Template `json:"label"`
	To    Template `json:"to"`
}

// Action calls a route the block's plugin declares, after a confirmation and a form if asked.
type Action struct {
	Label string `json:"label"`
	// Method is POST, PUT or DELETE.
	Method  string   `json:"method"`
	Path    Template `json:"path"`
	Confirm *Confirm `json:"confirm,omitempty"`
	// Form is sent as a JSON object of its values.
	Form []FormField `json:"form,omitempty"`
	// Invalidates names the data the action changes: loaded again once it succeeds.
	Invalidates []string `json:"invalidates,omitempty"`
	Danger      bool     `json:"danger,omitempty"`
	// Permission, Plugin and Scope are filled in by the dashboard from the route the path matches,
	// as for a data source.
	Permission string `json:"permission,omitempty"`
	Plugin     string `json:"plugin,omitempty"`
	Scope      string `json:"scope,omitempty"`
}

// Confirm asks before acting; Challenge, when set, must be typed back.
type Confirm struct {
	Text      Template `json:"text"`
	Challenge Template `json:"challenge,omitempty"`
}

// FormField is a value the action asks for.
type FormField struct {
	Name  string `json:"name"`
	Label string `json:"label"`
	// Type is text (the default), textarea, boolean or select.
	Type        string   `json:"type,omitempty"`
	Required    bool     `json:"required,omitempty"`
	OneOf       []string `json:"oneOf,omitempty"`
	Placeholder string   `json:"placeholder,omitempty"`
	Help        string   `json:"help,omitempty"`
}

// Empty says there is nothing to show.
type Empty struct {
	Text string `json:"text"`
}

// Use renders a composed widget, its parameters bound to data paths.
type Use struct {
	Widget string            `json:"widget"`
	With   map[string]string `json:"with,omitempty"`
}

// Tabs renders a navigation as a screen's tabs; Label names them for assistive technologies.
type Tabs struct {
	Navigation string `json:"navigation"`
	Label      string `json:"label,omitempty"`
}

// Outlet is where a framing screen shows its child.
type Outlet struct{}

// kinds lists the widget's set fields.
func (w Widget) kinds() []string {
	var set []string
	for name, present := range map[string]bool{
		"stack": w.Stack != nil, "grid": w.Grid != nil, "card": w.Card != nil,
		"heading": w.Heading != nil, "keyValue": w.KeyValue != nil, "table": w.Table != nil,
		"list": w.List != nil, "badge": w.Badge != nil, "stat": w.Stat != nil, "text": w.Text != nil,
		"markdown": w.Markdown != nil, "link": w.Link != nil, "action": w.Action != nil,
		"empty": w.Empty != nil, "use": w.Use != nil, "tabs": w.Tabs != nil, "outlet": w.Outlet != nil,
	} {
		if present {
			set = append(set, name)
		}
	}
	return set
}

// Walk visits the widget and every widget it contains, the actions of table columns included.
func (w *Widget) Walk(visit func(*Widget)) {
	visit(w)
	var children []Widget
	switch {
	case w.Stack != nil:
		children = w.Stack.Children
	case w.Grid != nil:
		children = w.Grid.Children
	case w.Card != nil:
		children = w.Card.Children
	case w.List != nil:
		w.List.Item.Walk(visit)
	case w.Table != nil:
		for i := range w.Table.Columns {
			if w.Table.Columns[i].Action != nil {
				(&Widget{Action: w.Table.Columns[i].Action}).Walk(visit)
			}
		}
	}
	for i := range children {
		children[i].Walk(visit)
	}
}

// Actions lists the widget's actions, wherever they are.
func (w *Widget) Actions() []*Action {
	var actions []*Action
	w.Walk(func(widget *Widget) {
		if widget.Action != nil {
			actions = append(actions, widget.Action)
		}
	})
	return actions
}

func (w Widget) problems(at string) []error {
	kinds := w.kinds()
	if len(kinds) != 1 {
		return []error{fmt.Errorf("%s: a widget is exactly one of stack, grid, card, heading, keyValue, table, list, badge, stat, text, markdown, link, action, empty, use, tabs, outlet (found %d)", at, len(kinds))}
	}
	problems := conditionProblems(at+".when", w.When)
	at += "." + kinds[0]
	children := func(widgets []Widget) {
		if len(widgets) == 0 {
			problems = append(problems, fmt.Errorf("%s.children: empty", at))
		}
		for i, child := range widgets {
			problems = append(problems, child.problems(fmt.Sprintf("%s.children[%d]", at, i))...)
		}
	}
	switch {
	case w.Stack != nil:
		children(w.Stack.Children)
	case w.Grid != nil:
		if w.Grid.Columns < 1 || w.Grid.Columns > 4 {
			problems = append(problems, fmt.Errorf("%s.columns: 1 to 4", at))
		}
		children(w.Grid.Children)
	case w.Card != nil:
		problems = append(problems, templateProblems(at+".title", w.Card.Title)...)
		children(w.Card.Children)
	case w.Heading != nil:
		problems = append(problems, requiredTemplate(at+".text", w.Heading.Text)...)
		if w.Heading.Level < 0 || w.Heading.Level > 3 {
			problems = append(problems, fmt.Errorf("%s.level: 1 to 3", at))
		}
	case w.KeyValue != nil:
		if len(w.KeyValue.Items) == 0 {
			problems = append(problems, fmt.Errorf("%s.items: empty", at))
		}
		for i, item := range w.KeyValue.Items {
			itemAt := fmt.Sprintf("%s.items[%d]", at, i)
			if item.Label == "" {
				problems = append(problems, fmt.Errorf("%s.label: missing", itemAt))
			}
			problems = append(problems, requiredTemplate(itemAt+".value", item.Value)...)
			problems = append(problems, templateProblems(itemAt+".link", item.Link)...)
		}
	case w.Table != nil:
		problems = append(problems, w.Table.problems(at)...)
	case w.List != nil:
		problems = append(problems, dataPathProblems(at+".items", w.List.Items)...)
		problems = append(problems, w.List.Item.problems(at+".item")...)
	case w.Badge != nil:
		problems = append(problems, w.Badge.problems(at)...)
	case w.Stat != nil:
		if w.Stat.Label == "" {
			problems = append(problems, fmt.Errorf("%s.label: missing", at))
		}
		problems = append(problems, requiredTemplate(at+".value", w.Stat.Value)...)
		problems = append(problems, templateProblems(at+".hint", w.Stat.Hint)...)
		problems = append(problems, w.Stat.Tone.problems(at+".tone")...)
	case w.Text != nil:
		problems = append(problems, requiredTemplate(at+".value", w.Text.Value)...)
	case w.Markdown != nil:
		problems = append(problems, requiredTemplate(at+".value", w.Markdown.Value)...)
	case w.Link != nil:
		problems = append(problems, requiredTemplate(at+".label", w.Link.Label)...)
		problems = append(problems, requiredTemplate(at+".to", w.Link.To)...)
	case w.Action != nil:
		problems = append(problems, w.Action.problems(at)...)
	case w.Empty != nil:
		if w.Empty.Text == "" {
			problems = append(problems, fmt.Errorf("%s.text: missing", at))
		}
	case w.Use != nil:
		if !ValidName(w.Use.Widget) {
			problems = append(problems, fmt.Errorf("%s.widget: %q is not a widget's name", at, w.Use.Widget))
		}
		for name, path := range w.Use.With {
			problems = append(problems, dataPathProblems(at+".with."+name, path)...)
		}
	case w.Tabs != nil:
		if !ValidName(w.Tabs.Navigation) {
			problems = append(problems, fmt.Errorf("%s.navigation: %q is not a navigation's name", at, w.Tabs.Navigation))
		}
	}
	return problems
}

func (t Table) problems(at string) []error {
	problems := dataPathProblems(at+".rows", t.Rows)
	if len(t.Columns) == 0 {
		problems = append(problems, fmt.Errorf("%s.columns: empty", at))
	}
	for i, column := range t.Columns {
		columnAt := fmt.Sprintf("%s.columns[%d]", at, i)
		set := 0
		if column.Value != "" {
			set++
		}
		if column.Badge != nil {
			set++
			problems = append(problems, column.Badge.problems(columnAt+".badge")...)
		}
		if column.Action != nil {
			set++
			problems = append(problems, column.Action.problems(columnAt+".action")...)
		}
		if set != 1 {
			problems = append(problems, fmt.Errorf("%s: a column is exactly one of value, badge, action", columnAt))
		}
		problems = append(problems, templateProblems(columnAt+".value", column.Value)...)
		problems = append(problems, templateProblems(columnAt+".link", column.Link)...)
		problems = append(problems, conditionProblems(columnAt+".when", column.When)...)
	}
	return problems
}

func (b Badge) problems(at string) []error {
	return append(requiredTemplate(at+".text", b.Text), b.Tone.problems(at+".tone")...)
}

func (t Tone) problems(at string) []error {
	var problems []error
	known := func(where, tone string) {
		for _, candidate := range Tones {
			if tone == candidate {
				return
			}
		}
		problems = append(problems, fmt.Errorf("%s: %q, expected one of %v", where, tone, Tones))
	}
	switch {
	case t.Fixed != "":
		known(at, t.Fixed)
	case t.From != "":
		problems = append(problems, dataPathProblems(at+".from", t.From)...)
		for value, tone := range t.Map {
			known(at+".map."+value, tone)
		}
		if t.Default != "" {
			known(at+".default", t.Default)
		}
	case len(t.Map) > 0 || t.Default != "":
		problems = append(problems, fmt.Errorf("%s: a tone read from the data names its field (from)", at))
	}
	return problems
}

var methods = map[string]bool{"POST": true, "PUT": true, "DELETE": true}

var fieldTypes = map[string]bool{"": true, "text": true, "textarea": true, "boolean": true, "select": true}

func (a Action) problems(at string) []error {
	var problems []error
	if a.Label == "" {
		problems = append(problems, fmt.Errorf("%s.label: missing", at))
	}
	if !methods[a.Method] {
		problems = append(problems, fmt.Errorf("%s.method: %q, expected POST, PUT or DELETE", at, a.Method))
	}
	problems = append(problems, requiredTemplate(at+".path", a.Path)...)
	if a.Permission != "" || a.Plugin != "" || a.Scope != "" {
		problems = append(problems, fmt.Errorf("%s: permission, plugin and scope are the route's, not the screen's to state", at))
	}
	if a.Confirm != nil {
		problems = append(problems, requiredTemplate(at+".confirm.text", a.Confirm.Text)...)
		problems = append(problems, templateProblems(at+".confirm.challenge", a.Confirm.Challenge)...)
	}
	names := map[string]bool{}
	for i, field := range a.Form {
		fieldAt := fmt.Sprintf("%s.form[%d]", at, i)
		switch {
		case !pathPattern.MatchString(field.Name) || names[field.Name]:
			problems = append(problems, fmt.Errorf("%s.name: %q is not a free field name", fieldAt, field.Name))
		case field.Label == "":
			problems = append(problems, fmt.Errorf("%s.label: missing", fieldAt))
		case !fieldTypes[field.Type]:
			problems = append(problems, fmt.Errorf("%s.type: %q, expected text, textarea, boolean or select", fieldAt, field.Type))
		case (field.Type == "select") != (len(field.OneOf) > 0):
			problems = append(problems, fmt.Errorf("%s: a select, and only a select, lists its values (oneOf)", fieldAt))
		}
		names[field.Name] = true
	}
	for i, name := range a.Invalidates {
		if !pathPattern.MatchString(name) {
			problems = append(problems, fmt.Errorf("%s.invalidates[%d]: %q is not a data name", at, i, name))
		}
	}
	return problems
}

func conditionProblems(at string, conditions []Condition) []error {
	var problems []error
	for i, condition := range conditions {
		conditionAt := fmt.Sprintf("%s[%d]", at, i)
		problems = append(problems, dataPathProblems(conditionAt+".field", condition.Field)...)
		tests := 0
		if len(condition.Equals) > 0 {
			tests++
		}
		if condition.In != nil {
			tests++
		}
		if condition.Present != nil {
			tests++
		}
		if tests != 1 {
			problems = append(problems, fmt.Errorf("%s: exactly one of equals, in, present", conditionAt))
		}
	}
	return problems
}

func dataPathProblems(at, path string) []error {
	if !pathPattern.MatchString(path) {
		return []error{fmt.Errorf("%s: %q is not a data path", at, path)}
	}
	return nil
}

func requiredTemplate(at string, template Template) []error {
	if template == "" {
		return []error{errors.New(at + ": missing")}
	}
	return templateProblems(at, template)
}
