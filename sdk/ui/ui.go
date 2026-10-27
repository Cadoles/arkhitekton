// Package ui is the schema of the dashboard's screens, described as resources rather than code.
//
// The core describes each of its screens, menus and tabs as a resource; an installed plugin brings
// resources of its own in its artifact. The dashboard merges them by name, the way Kubernetes
// merges an object applied by two managers: a plugin creates resources and entries under its own
// prefix (`<plugin>.`), and contributes to an existing resource by stating its name, adding blocks,
// menu entries or slot entries, never removing or replacing what another source owns.
//
// Three kinds:
//
//   - Screen: a route, the data it loads and its blocks, in order. A block is a named component
//     (`core/...` from the core, `<plugin>/...` registered by a plugin's script) or a declarative
//     widget, filled from the plugin's own routes through display templates.
//   - Navigation: the main menu, or the tabs of a screen.
//   - Widget: a widget composed of others, with parameters, that blocks reuse.
//
// The dashboard converts the YAML to JSON and reads it with this package; the composition itself
// is the dashboard's.
package ui

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// APIVersion is the schema version a resource states.
const APIVersion = "ui.arkhitekton/v1"

// Kind is a resource's kind.
type Kind string

const (
	KindScreen     Kind = "Screen"
	KindNavigation Kind = "Navigation"
	KindWidget     Kind = "Widget"
)

// Core is the owner of the dashboard's own resources, and the namespace of its components.
const Core = "core"

// Resource is one screen, navigation or widget, or a contribution to one.
type Resource struct {
	Kind Kind
	Name string
	// One of them, by Kind.
	Screen     *Screen
	Navigation *Navigation
	Widget     *WidgetDefinition
}

// Screen is a route and what it shows.
type Screen struct {
	// Path is relative to the parent's, in the router's syntax (`releases/:tag`). Absent, the screen
	// is its parent's index.
	Path string `json:"path,omitempty"`
	// Parent is the screen that frames this one: its blocks render around an outlet where this one
	// shows. Only the shell has none.
	Parent string `json:"parent,omitempty"`
	// Redirect sends the route elsewhere, an absolute path: an address kept for links already
	// shared.
	Redirect   string                `json:"redirect,omitempty"`
	Permission string                `json:"permission,omitempty"`
	Data       map[string]DataSource `json:"data,omitempty"`
	Blocks     []Block               `json:"blocks,omitempty"`
}

// Block is one part of a screen, or of a slot.
//
// A block states a component or a widget. One that states neither is a contribution to the block
// of that id: it may only fill its slots.
type Block struct {
	ID string `json:"id"`
	// After or Before places a block added to a list; absent, it goes last.
	After  string `json:"after,omitempty"`
	Before string `json:"before,omitempty"`

	Component string `json:"component,omitempty"`
	// Props are handed to the component as they are.
	Props  map[string]any `json:"props,omitempty"`
	Widget *Widget        `json:"widget,omitempty"`

	Data       map[string]DataSource `json:"data,omitempty"`
	When       []Condition           `json:"when,omitempty"`
	Permission string                `json:"permission,omitempty"`
	// Slots are where others add blocks inside a component: its owner declares them, empty or
	// not, and a slot nobody declared takes nothing.
	Slots map[string][]Block `json:"slots,omitempty"`
}

// Contribution says the block only fills the slots of the block of its id.
func (b Block) Contribution() bool { return b.Component == "" && b.Widget == nil }

// DataSource is what a screen or a block loads, under a name its templates read.
type DataSource struct {
	// Source is `core`, a read the dashboard offers (Name, Params), or `plugin`, a GET route the
	// block's plugin declares (Path).
	Source string            `json:"source"`
	Name   string            `json:"name,omitempty"`
	Params map[string]string `json:"params,omitempty"`
	Path   string            `json:"path,omitempty"`
}

// CoreSources are the reads a screen may name, with the parameters each takes.
var CoreSources = map[string][]string{
	"release":  {"tag"},
	"releases": {},
	"version":  {"id"},
	"run":      {"name"},
	"project":  {},
}

// Condition shows a block or a widget only when the data says so. Exactly one test.
type Condition struct {
	Field string `json:"field"`
	// Equals compares with a JSON value.
	Equals json.RawMessage `json:"equals,omitempty"`
	// In lists accepted values.
	In []json.RawMessage `json:"in,omitempty"`
	// Present asks for a value that is there and not empty (true), or one that is not (false).
	Present *bool `json:"present,omitempty"`
}

// Navigation is a menu: the main one, or a screen's tabs.
type Navigation struct {
	// Home is where `/` leads; absent, the releases.
	Home  string    `json:"home,omitempty"`
	Items []NavItem `json:"items,omitempty"`
}

// NavItem is one entry.
type NavItem struct {
	ID     string `json:"id"`
	After  string `json:"after,omitempty"`
	Before string `json:"before,omitempty"`
	Label  string `json:"label"`
	Icon   string `json:"icon,omitempty"`
	// To is an absolute path, a display template over the route's parameters (`{route.slug}`).
	To string `json:"to"`
	// End marks the entry current only on its exact path: the index tab.
	End        bool   `json:"end,omitempty"`
	Permission string `json:"permission,omitempty"`
}

// WidgetDefinition is a composed widget.
type WidgetDefinition struct {
	Params map[string]Param `json:"params,omitempty"`
	Widget Widget           `json:"widget"`
}

// Param is a composed widget's parameter: a data path its user binds.
type Param struct {
	Required bool `json:"required,omitempty"`
}

var (
	namePattern      = regexp.MustCompile(`^[a-z][a-z0-9-]*(\.[a-z][a-z0-9-]*)*$`)
	componentPattern = regexp.MustCompile(`^[a-z][a-z0-9-]*/[a-z][a-z0-9-]*$`)
	pathPattern      = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*(\.[A-Za-z0-9_]+)*$`)
)

// ValidName says whether a resource, a block or an entry may bear the name.
func ValidName(name string) bool { return namePattern.MatchString(name) }

// Owner is the source a name says it belongs to: what precedes its first dot, or the whole.
func Owner(name string) string {
	owner, _, _ := strings.Cut(name, ".")
	return owner
}

// ComponentOwner is the namespace of a component's name.
func ComponentOwner(component string) string {
	owner, _, _ := strings.Cut(component, "/")
	return owner
}

type document struct {
	APIVersion string          `json:"apiVersion"`
	Kind       Kind            `json:"kind"`
	Metadata   metadata        `json:"metadata"`
	Spec       json.RawMessage `json:"spec"`
}

type metadata struct {
	Name string `json:"name"`
}

// Decode reads one resource from JSON, refusing unknown fields, and validates its structure.
func Decode(raw []byte) (Resource, error) {
	var doc document
	if err := strictDecode(raw, &doc); err != nil {
		return Resource{}, err
	}
	if doc.APIVersion != APIVersion {
		return Resource{}, fmt.Errorf("apiVersion %q, expected %s", doc.APIVersion, APIVersion)
	}
	resource := Resource{Kind: doc.Kind, Name: doc.Metadata.Name}
	var target any
	switch doc.Kind {
	case KindScreen:
		resource.Screen = &Screen{}
		target = resource.Screen
	case KindNavigation:
		resource.Navigation = &Navigation{}
		target = resource.Navigation
	case KindWidget:
		resource.Widget = &WidgetDefinition{}
		target = resource.Widget
	default:
		return Resource{}, fmt.Errorf("kind %q: Screen, Navigation or Widget", doc.Kind)
	}
	if len(doc.Spec) == 0 {
		return Resource{}, fmt.Errorf("%s %s: spec is missing", doc.Kind, doc.Metadata.Name)
	}
	if err := strictDecode(doc.Spec, target); err != nil {
		return Resource{}, fmt.Errorf("%s %s: spec: %w", doc.Kind, doc.Metadata.Name, err)
	}
	if err := resource.Validate(); err != nil {
		return Resource{}, err
	}
	return resource, nil
}

func strictDecode(raw []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if decoder.More() {
		return errors.New("trailing content")
	}
	return nil
}

// MarshalJSON writes the resource back in its document form.
func (r Resource) MarshalJSON() ([]byte, error) {
	var spec any
	switch r.Kind {
	case KindScreen:
		spec = r.Screen
	case KindNavigation:
		spec = r.Navigation
	case KindWidget:
		spec = r.Widget
	}
	raw, err := json.Marshal(spec)
	if err != nil {
		return nil, err
	}
	return json.Marshal(document{APIVersion: APIVersion, Kind: r.Kind, Metadata: metadata{Name: r.Name}, Spec: raw})
}
