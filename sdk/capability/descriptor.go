package capability

/*
 * What a plugin says about itself: the settings it expects, the facts it observes and the steps it
 * can carry out. The dashboard reads these to compose its administration screen, its reaction
 * editor and the validation of both.
 */

// FieldType says how a field is typed in and checked.
type FieldType string

const (
	FieldText FieldType = "text"
	FieldURL  FieldType = "url"
	FieldBool FieldType = "bool"
	// FieldList is a comma-separated list, stored as typed. The format matches the
	// environment variables it replaces, so a migrated configuration is copied
	// across without translation.
	FieldList FieldType = "list"
)

// Field declares one setting a plugin expects.
type Field struct {
	Name  string    `json:"name"`
	Label string    `json:"label"`
	Type  FieldType `json:"type"`
	// Help is read under the field. It is what keeps a project identifier from
	// being filled in with the project's label.
	Help string `json:"help,omitempty"`
	// Required applies to the scope where the field is declared. A field required
	// on a project may be left empty when the instance provides it: what must
	// exist is the resolved value, not the typed one.
	Required bool `json:"required,omitempty"`
	// Secret marks a value sealed at rest and never returned.
	Secret      bool   `json:"secret,omitempty"`
	Placeholder string `json:"placeholder,omitempty"`
}

// EventSpec declares a fact this plugin can observe. Without it, reactions could
// only ever be hung off causes the core itself produces.
type EventSpec struct {
	// Suffix is the type without its prefix: `pull_request.opened`. It is declared
	// bare and prefixed by [Plugin.EventType], so that two forge plugins
	// cannot produce indistinguishable facts.
	Suffix string `json:"suffix"`
	Label  string `json:"label"`
	// Payload enumerates the fields of the payload. Declared rather than free-form
	// so the editor can offer them and templates can be validated when saved,
	// instead of failing at runtime on a key that does not exist.
	Payload []Field `json:"payload"`
}

// ActionField declares a parameter an action takes.
//
// Deliberately distinct from [Field]: a setting is typed once per project and
// governs everything the plugin does, while an action parameter is typed per
// step and accepts templates bound to the triggering fact's payload.
type ActionField struct {
	Name  string `json:"name"`
	Label string `json:"label"`
	Help  string `json:"help,omitempty"`
	// Required: the value must be there, possibly after interpolation.
	Required bool `json:"required,omitempty"`
	// Template says the value accepts `{key}` variables bound to the payload of
	// the triggering fact.
	Template bool `json:"template,omitempty"`
	// OneOf closes the set of accepted values. Declared by the plugin rather than
	// hard-coded in validation: the states of a commit status are a forge's
	// vocabulary, so a second forge with different ones must not require editing
	// the validator.
	OneOf       []string `json:"one_of,omitempty"`
	Placeholder string   `json:"placeholder,omitempty"`
}

// ActionSpec declares a step a plugin can carry out — the counterpart of
// [EventSpec].
//
// A plugin action takes no secret field, and that is structural: it talks to its
// own boundary, whose connection is already configured and sealed. It therefore
// cannot be used to send a secret to a target chosen in a form, because it does
// not choose its target.
type ActionSpec struct {
	// Suffix is the reference without its prefix: `status`, `comment`. Prefixed by
	// [Plugin.ActionRef], for the same reason facts are.
	Suffix string `json:"suffix"`
	Label  string `json:"label"`
	// Summary says what the action does, in one sentence, in the editor.
	Summary string        `json:"summary"`
	Fields  []ActionField `json:"fields"`
}

// EventType composes the full type of one of this plugin's facts: `gitea` plus
// `pull_request.opened` gives `gitea.pull_request.opened`. Two forge plugins may coexist, and a
// reaction must say which one it listens to.
func (p Plugin) EventType(spec EventSpec) string { return p.ID + "." + spec.Suffix }

// ActionRef returns an action's full reference, prefixed by the plugin.
func (p Plugin) ActionRef(spec ActionSpec) string { return p.ID + "." + spec.Suffix }

// Observes says whether the plugin declares at least one fact.
func (p Plugin) Observes() bool { return len(p.Events) > 0 }

// Acts says whether the plugin declares at least one action.
func (p Plugin) Acts() bool { return len(p.Actions) > 0 }

// Headers lists every capability the plugin brings: its pipelines, its tracker and forge, then
// what the dashboard implements on its behalf.
func (p Plugin) Headers() []Header {
	headers := make([]Header, 0, len(p.Pipelines)+len(p.Capabilities)+2)
	for _, pipeline := range p.Pipelines {
		headers = append(headers, Header{Kind: KindPipeline, Name: pipeline.Name, Label: pipeline.Label})
	}
	if p.Tracker != nil {
		headers = append(headers, Header{Kind: KindTracker, Name: "tracker", Label: p.Label})
	}
	if p.Forge != nil {
		headers = append(headers, Header{Kind: KindForge, Name: "forge", Label: p.Label})
	}
	return append(headers, p.Capabilities...)
}

// HasCapability says whether the plugin brings a capability of this kind.
func (p Plugin) HasCapability(kind Kind) bool {
	for _, header := range p.Headers() {
		if header.Kind == kind {
			return true
		}
	}
	return false
}
