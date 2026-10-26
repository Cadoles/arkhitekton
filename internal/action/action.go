// Package action declares the actions a plan can carry out.
//
// # A registry, not an engine
//
// This package says what an action **expects** and checks that it is given. It does not run it:
// leaving the cluster needs a controlled client, which lives in `internal/webhook`, and mixing it
// in here would make this registry a dependency of everything that only wants to read the list of
// actions — the editor, notably.
//
// It is the same split as for plugins: the declaration on one side, the execution on the other.
//
// # One internal action, and the others come from plugins
//
// [Registry] declares only one: the outgoing call. It is what remains once what belongs to a
// boundary is removed — setting a commit status is an act of a **forge**, and declaring it here
// would mean adding a second forge required modifying this package.
//
// A chat notification is not a second action: Mattermost, Slack and the rest are webhooks, and
// giving them a dedicated action would be the same code three times under three names.
//
// [Catalog] brings both registries together. That is the list the editor and the validation see; the
// distinction between a core action and a plugin action is none of their business.
//
// What is still not there and is worth naming: writing into the tracker. The Redmine plugin's
// transport structurally refuses any method other than GET and HEAD, and that is a documented
// choice — taken because the key targeted is that of the client's production. Opening it is a
// separate piece of work.
package action

import (
	"errors"
	"fmt"
	"net/url"
	"slices"
	"sort"
	"strings"

	"forge.cadoles.com/vcarroy/arkhitekton/sdk/capability"
)

// Field declares a parameter an action expects.
//
// The same shape as a plugin's action fields, and for the same reason: it is what lets the
// editor compose a form, and the validation refuse a faulty entry at recording time rather than
// at run time.
type Field struct {
	Name  string `json:"name"`
	Label string `json:"label"`
	Help  string `json:"help,omitempty"`
	// Required: the value must be there, possibly after interpolating the fact's context.
	Required bool `json:"required,omitempty"`
	// Template says the value allows `{key}` variables, bound to the triggering fact's payload.
	Template bool `json:"template,omitempty"`
	// SecretRef says the value **names** a plugin's sensitive field, and never carries its content.
	//
	// It is the rule made structural: a sensitive field is never a template variable. It can only be
	// named, and the engine sets it as a header without ever making it readable.
	SecretRef bool `json:"secret_ref,omitempty"`
	// OneOf closes the set of accepted values. Empty, the value is free.
	OneOf       []string `json:"one_of,omitempty"`
	Placeholder string   `json:"placeholder,omitempty"`
}

// Descriptor declares an action.
type Descriptor struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	// Summary says what it does, in one sentence, in the editor.
	Summary string  `json:"summary"`
	Fields  []Field `json:"fields"`
	// Plugin names the plugin supplying the action, empty for a core action.
	//
	// Returned to the editor so that it can say where an act comes from, and read by the runtime to
	// know whom to hand it to.
	Plugin string `json:"plugin,omitempty"`
}

// Webhook issues a JSON POST to a URL. The core's only action.
const Webhook = "webhook"

// Registry lists the internal actions.
//
// A function and not a variable: returning a shared slice would let a caller sort it or write
// into it, and the next registry would inherit that.
func Registry() []Descriptor {
	descriptors := []Descriptor{
		{
			ID: Webhook, Label: "Appel sortant",
			Summary: "Émet un POST JSON vers une URL. C'est aussi la notification " +
				"de messagerie : Mattermost, Slack et les autres sont des webhooks.",
			Fields: []Field{
				{
					Name: "url", Label: "URL", Required: true,
					Help: "En HTTPS. Les cibles privées et la boucle locale sont " +
						"refusées, sauf autorisation explicite du déploiement.",
					Placeholder: "https://hooks.example.fr/services/…",
				},
				{
					Name: "body", Label: "Corps", Template: true,
					Help: "JSON, avec des variables `{tag}`, `{outcome}` liées à la " +
						"charge du fait. Vide, la charge entière est envoyée.",
				},
				{
					Name: "auth_header", Label: "En-tête d'authentification",
					Help:        "Nom de l'en-tête qui porte le jeton — `Authorization`.",
					Placeholder: "Authorization",
				},
				{
					Name: "auth_secret", Label: "Jeton", SecretRef: true,
					Help: "**Désigne** un champ sensible d'un plugin ; n'en porte " +
						"jamais le contenu. Le jeton est posé dans l'en-tête sans " +
						"passer par le corps ni par un journal.",
				},
			},
		},
	}

	sort.Slice(descriptors, func(i, j int) bool { return descriptors[i].ID < descriptors[j].ID })
	return descriptors
}

// Catalog brings together the core's actions and those the plugins declare.
//
// # The registry opened in the other direction
//
// The plugins already declared the facts they observe; they also declare the acts they can
// perform. Without that half, any **cause** is wired to a frozen set of **consequences**, and
// acting on a boundary the core does not know requires modifying the core again.
//
// # Without filtering on activation
//
// A switched-off plugin keeps its actions in this list. That is deliberate: validating a plan
// **frozen** into a run must stay possible after it is switched off, otherwise a history view
// would lose its labels. It is for the editor to set aside what would not trigger — it already
// does so for the facts, and for the same reason.
type Catalog struct {
	plugins []capability.Plugin
}

// NewCatalog gathers the core's actions and the plugins'.
func NewCatalog(plugins []capability.Plugin) Catalog {
	return Catalog{plugins: plugins}
}

// All lists every action, sorted.
func (c Catalog) All() []Descriptor {
	descriptors := Registry()
	for _, provider := range c.plugins {
		for _, spec := range provider.Actions {
			descriptors = append(descriptors, fromPlugin(provider, spec))
		}
	}

	sort.Slice(descriptors, func(i, j int) bool { return descriptors[i].ID < descriptors[j].ID })
	return descriptors
}

// fromPlugin translates a plugin's declaration into an action descriptor.
//
// The translation is narrow, and the field it does not have to carry says why: [Field.SecretRef]
// stays false. A plugin action speaks to its own boundary, whose connection is already sealed; it
// names no token, and therefore cannot serve to extract one towards a target typed into a form.
func fromPlugin(provider capability.Plugin, spec capability.ActionSpec) Descriptor {
	fields := make([]Field, 0, len(spec.Fields))
	for _, field := range spec.Fields {
		fields = append(fields, Field{
			Name: field.Name, Label: field.Label, Help: field.Help,
			Required: field.Required, Template: field.Template,
			OneOf: field.OneOf, Placeholder: field.Placeholder,
		})
	}

	return Descriptor{
		ID: provider.ActionRef(spec), Label: spec.Label, Summary: spec.Summary,
		Fields: fields, Plugin: provider.ID,
	}
}

// ErrUnknown reports an action no descriptor declares.
var ErrUnknown = errors.New("unknown action")

// Get returns an action's descriptor, from the core or from a plugin.
//
// The error names those that exist: hunting for which one in the source code is not an answer to
// give to whoever reads an API message.
func (c Catalog) Get(id string) (Descriptor, error) {
	registry := c.All()
	for _, descriptor := range registry {
		if descriptor.ID == id {
			return descriptor, nil
		}
	}

	known := make([]string, 0, len(registry))
	for _, descriptor := range registry {
		known = append(known, descriptor.ID)
	}
	return Descriptor{}, fmt.Errorf("%w %q (declared: %s)",
		ErrUnknown, id, strings.Join(known, ", "))
}

// ErrInvalid reports parameters that are refused.
var ErrInvalid = errors.New("invalid action parameters")

// Validate refuses parameters inconsistent with the descriptor.
//
// Checked when a plan is recorded and not when it runs: a faulty act discovered at the moment of
// starting leaves a chain stopped, and the diagnosis then happens on a waiting release rather
// than in front of a form.
func (d Descriptor) Validate(params map[string]string) error {
	declared := make(map[string]Field, len(d.Fields))
	for _, field := range d.Fields {
		declared[field.Name] = field
	}

	var problems []error

	// An unknown parameter is refused rather than ignored: ignored, it gives a screen that says
	// "recorded" on a value nothing will read back.
	names := make([]string, 0, len(params))
	for name := range params {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if _, known := declared[name]; !known {
			problems = append(problems, fmt.Errorf(
				"%w: the action %q declares no parameter %q", ErrInvalid, d.ID, name))
		}
	}

	for _, field := range d.Fields {
		value := strings.TrimSpace(params[field.Name])
		if field.Required && value == "" {
			problems = append(problems, fmt.Errorf("%w: %s is required", ErrInvalid, field.Label))
			continue
		}
		if value == "" {
			continue
		}
		problems = append(problems, field.validate(value)...)
	}

	return errors.Join(problems...)
}

func (f Field) validate(value string) []error {
	var problems []error

	// The URL's shape is checked, not its target: the target is judged when connecting, on the
	// resolved address, by the transport. The two checks do not replace each other — this one catches
	// a typo, the second catches a forgery.
	if f.Name == "url" {
		parsed, err := url.Parse(value)
		switch {
		case err != nil || parsed.Host == "":
			problems = append(problems, fmt.Errorf(
				"%w: %s must be an absolute URL, received %q", ErrInvalid, f.Label, value))
		case parsed.Scheme != "https" && parsed.Scheme != "http":
			problems = append(problems, fmt.Errorf(
				"%w: %s: scheme %q (expected https)", ErrInvalid, f.Label, parsed.Scheme))
		}
	}

	// A closed set is checked here rather than at the recipient's: the state of a commit status
	// belongs to the forge's vocabulary, and an invented value would be refused by it — at the least
	// useful moment.
	//
	// The list comes from the descriptor and not from a condition on the action's identifier: that is
	// what lets a second forge plugin have its own states without this package having to know it.
	//
	// Skipped when the value carries a template: what it will be worth depends on the fact's payload,
	// and refusing `{outcome}` here would force the state to be written literally.
	if len(f.OneOf) > 0 && !strings.Contains(value, "{") && !slices.Contains(f.OneOf, value) {
		problems = append(problems, fmt.Errorf(
			"%w: %s is one of %s, received %q",
			ErrInvalid, f.Label, strings.Join(f.OneOf, ", "), value))
	}

	// A template with an unclosed brace would produce a literal value the recipient could not
	// understand.
	if f.Template && strings.Count(value, "{") != strings.Count(value, "}") {
		problems = append(problems, fmt.Errorf(
			"%w: %s: unmatched braces", ErrInvalid, f.Label))
	}

	return problems
}

// Expand replaces the `{key}` variables with the fact's payload.
//
// # A brace is not always a variable
//
// A webhook's body is **JSON**, and JSON is made of braces. A naive sweep — first opening brace,
// first closing one — would make `{"release":"{tag}"` a variable name, swallow the real one and
// leave the template literal. That is this function's main case, not a contrived one.
//
// Only braces whose content is a **plausible name** are therefore recognised as variables:
// letters, digits, `_`, `.`, `-`. Everything else — a JSON structural brace, a fragment — passes
// through as it is.
//
// # An unknown variable stays literal
//
// Rather than replaced by nothing, and that is deliberate: a `{tag}` visible in a chat message is
// noticed and fixed, whereas a silent hole reads as a release with no name — and nobody could
// say whether it is the template or the data that is missing.
func Expand(template string, values map[string]string) string {
	if !strings.Contains(template, "{") {
		return template
	}

	var out strings.Builder
	out.Grow(len(template))

	for index := 0; index < len(template); {
		if template[index] != '{' {
			out.WriteByte(template[index])
			index++
			continue
		}

		end := strings.IndexByte(template[index:], '}')
		if end < 0 {
			// No closing brace left: the rest is literal.
			out.WriteString(template[index:])
			break
		}
		end += index

		name := template[index+1 : end]
		if !plausibleName(name) {
			// A structural brace. This one is written and we resume just after it: the closing brace just
			// seen may belong to a real variable, and consuming it here would make it disappear.
			out.WriteByte('{')
			index++
			continue
		}

		if value, known := values[name]; known {
			out.WriteString(value)
		} else {
			out.WriteString(template[index : end+1])
		}
		index = end + 1
	}

	return out.String()
}

// plausibleName says whether the content of a pair of braces can be a variable name.
//
// Deliberately narrow: better to let through a brace that could have been a variable — it is
// visible and gets fixed — than to interpret a JSON structure as an interpolation, which would
// produce a mute payload.
func plausibleName(name string) bool {
	if name == "" {
		return false
	}
	for _, letter := range name {
		switch {
		case letter >= 'a' && letter <= 'z',
			letter >= 'A' && letter <= 'Z',
			letter >= '0' && letter <= '9',
			letter == '_', letter == '.', letter == '-':
		default:
			return false
		}
	}
	return true
}
