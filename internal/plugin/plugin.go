// Package plugin declares the extensions this binary carries and the settings
// each one expects. See docs/06-plugins.md.
//
// The registry is filled at compile time: no init(), no dynamic resolution,
// nothing loaded at runtime. Installing a plugin means rebuilding the binary.
//
// Settings have two scopes. Global values are the instance defaults; project
// values are its departures from them. An empty project value inherits rather
// than erases, which is what lets a single-tenant deployment configure nothing
// twice while a second tenant declares its own server.
package plugin

import (
	"errors"
	"fmt"
	"maps"
	"net/url"
	"sort"
	"strings"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/capability"
)

// ModuleMSE identifies the MSE plugin: environments, Flux promotion, PortalData
// attestation.
const ModuleMSE = "mse"

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
	// bare and prefixed by [Descriptor.EventType], so that two forge plugins
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
	// [Descriptor.ActionRef], for the same reason facts are.
	Suffix string `json:"suffix"`
	Label  string `json:"label"`
	// Summary says what the action does, in one sentence, in the editor.
	Summary string        `json:"summary"`
	Fields  []ActionField `json:"fields"`
}

// Descriptor declares a plugin and its settings.
type Descriptor struct {
	ID string `json:"id"`
	// Capabilities say what the plugin brings. The kind qualifies a capability, not the plugin: one
	// plugin may bring a pipeline and a tracker.
	Capabilities []capability.Header `json:"capabilities"`
	Label        string              `json:"label"`
	// Summary says what the plugin does, in one sentence, on the administration
	// screen.
	Summary string `json:"summary"`
	// Global lists the instance settings, which serve as defaults.
	Global []Field `json:"global"`
	// Project lists the settings a project may set or override. Empty does not
	// preclude PerProject: a plugin can be chosen per project with nothing to
	// configure.
	Project []Field `json:"project"`
	// Events lists the facts this plugin observes. Empty for one that observes
	// nothing.
	Events []EventSpec `json:"events,omitempty"`
	// Actions lists the steps this plugin can carry out. Observing and acting are
	// separate capabilities: lending actions to an attester for symmetry would
	// display steps that nothing executes.
	Actions []ActionSpec `json:"actions,omitempty"`
	// PerProject says a project decides whether this plugin is enabled. False for
	// what necessarily holds for the whole instance, where a switch would do
	// nothing.
	PerProject bool `json:"per_project"`
}

// HasCapability says whether the plugin brings a capability of this kind.
func (d Descriptor) HasCapability(kind capability.Kind) bool {
	for _, header := range d.Capabilities {
		if header.Kind == kind {
			return true
		}
	}
	return false
}

// Registry lists the plugins this binary carries.
//
// A function rather than a variable: returning a shared slice would let a caller
// sort or write into it, and the next caller would inherit that.
func Registry() []Descriptor {
	descriptors := []Descriptor{
		{
			ID:           "redmine",
			Capabilities: []capability.Header{{Kind: capability.KindTracker, Name: "tracker", Label: "Suivi"}},
			Label:        "Redmine",
			Summary:      "Versions, demandes et avancement lus dans une instance Redmine.",
			// One plugin-specific fact only. The probe observes two, but the new
			// version is a core fact — `version.created`, unprefixed — because every
			// tracker writes versions to the same table, so a reaction on it need not
			// be rewritten when the tracker changes.
			//
			// A closed issue stays here: it has no core equivalent, and it is an
			// event — something that happened once. A status change is not: it
			// happens ten times, and one fact per change would turn the feed into a
			// keystroke log.
			Events: []EventSpec{
				{
					Suffix: "issue.closed",
					Label:  "Demande close",
					Payload: []Field{
						{Name: "issue_id", Label: "Numéro"},
						{Name: "subject", Label: "Sujet"},
						{Name: "tracker", Label: "Type"},
						{Name: "status", Label: "Statut"},
						{Name: "version_id", Label: "Version visée"},
					},
				},
			},
			// The connection is declared at both scopes: globally as the default,
			// per project so a second tenant can have its own server without the
			// first having to type anything.
			Global: []Field{
				{
					Name: "url", Label: "URL de l'instance", Type: FieldURL,
					Help:        "Racine de Redmine, sans chemin.",
					Placeholder: "https://redmine.example.fr",
				},
				{
					Name: "api_key", Label: "Clé d'API", Type: FieldText, Secret: true,
					Help: "Lecture seule suffit : le dashboard consulte, il n'écrit " +
						"jamais dans le suivi. La clé est scellée en base et n'est " +
						"jamais réaffichée.",
				},
			},
			Project: []Field{
				{
					Name: "project", Label: "Identifiant du projet", Type: FieldText,
					Required: true,
					Help: "Tel qu'il apparaît dans l'URL Redmine, et non son " +
						"libellé.",
					Placeholder: "mon-projet",
				},
				{
					Name: "review_statuses", Label: "Statuts de revue", Type: FieldList,
					Help: "Statuts qui rendent une demande éligible à la note de " +
						"release. Le vocabulaire appartient au client.",
					Placeholder: "En revue",
				},
				{
					Name: "url", Label: "URL de l'instance", Type: FieldURL,
					Help:        "Vide : celle de l'instance s'applique.",
					Placeholder: "https://redmine.example.fr",
				},
				{
					Name: "api_key", Label: "Clé d'API", Type: FieldText, Secret: true,
					Help: "Vide : celle de l'instance s'applique.",
				},
			},
			PerProject: true,
		},
		{
			ID:           "gitea",
			Capabilities: []capability.Header{{Kind: capability.KindForge, Name: "forge", Label: "Forge"}},
			Label:        "Gitea",
			Summary: "Branches, tags et références proposés au formulaire de release, " +
				"détection des demandes de fusion ouvertes, et retour vers la forge.",
			Actions: []ActionSpec{
				{
					Suffix: "status", Label: "Statut de commit",
					Summary: "Pose un statut sur un commit, visible dans la forge à côté " +
						"de la demande de fusion.",
					Fields: []ActionField{
						{
							Name: "commit", Label: "Commit", Required: true, Template: true,
							Help: "Le SHA visé. `{git_commit}` pour celui de la " +
								"construction qui vient de conclure.",
							Placeholder: "{git_commit}",
						},
						{
							Name: "state", Label: "État", Required: true,
							OneOf: []string{"pending", "success", "failure", "error"},
							Help: "Le vocabulaire de la forge, et il est fermé. Ce " +
								"n'est **pas** celui d'un fait : `{outcome}` rend " +
								"`succeeded`, que la forge accepte sans savoir " +
								"l'afficher. Pour refléter l'issue, écrire l'état en " +
								"dur et apparier la réaction sur `outcome`.",
							Placeholder: "success",
						},
						{
							Name: "context", Label: "Contexte", Required: true,
							Help: "Le nom sous lequel le statut apparaît. Deux gestes " +
								"partageant ce nom se remplacent : c'est ainsi qu'un " +
								"`pending` devient un `success`.",
							Placeholder: "arkhitekton/vérifications",
						},
						{
							Name: "description", Label: "Description", Template: true,
							Help: "Une ligne, avec des variables `{tag}`, `{outcome}`.",
						},
						{
							Name: "target_url", Label: "Lien", Template: true,
							Help: "Où mène le statut depuis la forge. Vide, il ne mène " +
								"nulle part et oblige à revenir ici à la main.",
						},
					},
				},
				{
					Suffix: "comment", Label: "Commentaire sur une demande de fusion",
					Summary: "Ajoute un commentaire à une demande de fusion. Le numéro " +
						"vient du fait qui l'a fait connaître.",
					Fields: []ActionField{
						{
							Name: "number", Label: "Numéro", Required: true, Template: true,
							Help: "`{number}`, tel que le fait « demande de fusion " +
								"ouverte » le porte.",
							Placeholder: "{number}",
						},
						{
							Name: "body", Label: "Message", Required: true, Template: true,
							Help: "Markdown, avec les variables de la charge du fait.",
						},
					},
				},
			},
			Events: []EventSpec{{
				Suffix: "pull_request.opened",
				Label:  "Demande de fusion ouverte",
				Payload: []Field{
					{Name: "number", Label: "Numéro"},
					{Name: "title", Label: "Titre"},
					{Name: "author", Label: "Auteur"},
					{Name: "source_branch", Label: "Branche source"},
					{Name: "target_branch", Label: "Branche cible"},
					{Name: "url", Label: "Lien", Type: FieldURL},
				},
			}},
			Global: []Field{
				{
					Name: "url", Label: "URL de la forge", Type: FieldURL,
					Placeholder: "https://forge.example.fr",
				},
				{
					Name: "token", Label: "Jeton", Type: FieldText, Secret: true,
					Help: "Une portée `read:repository` suffit pour lire et observer, " +
						"et vaut mieux : un jeton complet ouvre la forge entière au " +
						"nom de son détenteur. `write:repository` n'est nécessaire " +
						"que pour poser des tags, des statuts ou des commentaires.",
				},
			},
			// No per-project settings: the repository is already declared in the
			// project's own parameters, and asking again here would make two places
			// to correct it — so one place to forget.
			//
			// Per-project activation, though: watching a repository's pull requests
			// is a project's decision, even when the connection is instance-wide.
			PerProject: true,
		},
		{
			ID:           ModuleMSE,
			Capabilities: []capability.Header{{Kind: capability.KindModule, Name: "module", Label: "Module"}},
			Label:        "MSE",
			Summary:      "Environnements, promotion par Flux et attestation par PortalData.",
			Global: []Field{
				{
					Name: "namespace", Label: "Namespace Flux", Type: FieldText,
					Help:        "Là où vivent les objets Flux, distinct de celui des pipelines.",
					Placeholder: "flux-system",
				},
				{
					Name: "kustomRepoUrl", Label: "Dépôt de kustomisation", Type: FieldText,
					Help:        "Le dépôt que les pipelines clonent pour déployer.",
					Placeholder: "ssh://git@forge.example:2222/org/kustom.git",
				},
				{
					Name: "kustomRevision", Label: "Révision de la kustomisation", Type: FieldText,
					Help:        "La branche auditée et déployée.",
					Placeholder: "main",
				},
				{
					Name: "e2eRepoUrl", Label: "Dépôt des tests E2E", Type: FieldText,
					Placeholder: "ssh://git@forge.example:2222/org/tests-e2e.git",
				},
				{
					Name: "overlayLocation", Label: "Emplacement de l'overlay", Type: FieldText,
					Help: "Cherché dans la kustomisation, et non dans l'application : " +
						"viser la racine de l'une parce que l'autre y porte son Dockerfile " +
						"a coûté un déploiement.",
					Placeholder: "overlays/pipeline",
				},
			},
			// A project may deploy through a Flux other than the instance's, with its
			// ImagePolicy and Kustomization objects in another namespace. Empty
			// inherits.
			Project: []Field{
				{
					Name: "namespace", Label: "Namespace Flux", Type: FieldText,
					Help:        "Là où vivent les objets Flux de ce projet. Vide : celui de l'instance.",
					Placeholder: "flux-system",
				},
				{
					Name: "kustomRepoUrl", Label: "Dépôt de kustomisation", Type: FieldText,
					Help:        "Le dépôt que les pipelines clonent pour déployer.",
					Placeholder: "ssh://git@forge.example:2222/org/kustom.git",
				},
				{
					Name: "kustomRevision", Label: "Révision de la kustomisation", Type: FieldText,
					Help:        "La branche auditée et déployée.",
					Placeholder: "main",
				},
				{
					Name: "e2eRepoUrl", Label: "Dépôt des tests E2E", Type: FieldText,
					Placeholder: "ssh://git@forge.example:2222/org/tests-e2e.git",
				},
				{
					Name: "overlayLocation", Label: "Emplacement de l'overlay", Type: FieldText,
					Help: "Cherché dans la kustomisation, et non dans l'application : " +
						"viser la racine de l'une parce que l'autre y porte son Dockerfile " +
						"a coûté un déploiement.",
					Placeholder: "overlays/pipeline",
				},
			},
			// Promotion is a plugin fact, not a core one: the plugin defines what a
			// promotion is, so the plugin observes it.
			Events: []EventSpec{
				{
					Suffix: "release.promoted",
					Label:  "Promotion aboutie",
					Payload: []Field{
						{Name: "release_id", Label: "Release"},
						{Name: "tag", Label: "Tag"},
						{Name: "environment", Label: "Environnement"},
						{Name: "job_id", Label: "Job"},
					},
				},
			},
			// Per project: only a project that belongs to the MSE portal gets it. The
			// instance switch is both permission and default — switched off there, no
			// project can switch it back on.
			PerProject: true,
		},
		{
			ID:           "bon-de-livraison",
			Capabilities: []capability.Header{{Kind: capability.KindExporter, Name: "exporter", Label: "Livrables"}},
			Label:        "Bon de livraison",
			Summary:      "Livrable contractuel récapitulant le périmètre et les vérifications.",
			// Per project: it used to be offered on every release, including those of
			// projects that produce no such deliverable.
			PerProject: true,
		},
	}

	sort.Slice(descriptors, func(i, j int) bool {
		return descriptors[i].ID < descriptors[j].ID
	})
	return descriptors
}

// ErrUnknown reports an id no plugin carries.
var ErrUnknown = errors.New("unknown plugin")

// Get returns a plugin's descriptor. The error names the known plugins: reading
// the source to find out which exist is not an answer to give someone looking at
// an API message.
func Get(id string) (Descriptor, error) {
	registry := Registry()
	for _, descriptor := range registry {
		if descriptor.ID == id {
			return descriptor, nil
		}
	}

	known := make([]string, 0, len(registry))
	for _, descriptor := range registry {
		known = append(known, descriptor.ID)
	}
	return Descriptor{}, fmt.Errorf("%w %q (embedded: %s)",
		ErrUnknown, id, strings.Join(known, ", "))
}

// EventType composes the full type of one of this plugin's facts: `gitea` plus
// `pull_request.opened` gives `gitea.pull_request.opened`. Two forge plugins will
// coexist, and a reaction must say which one it listens to.
func (d Descriptor) EventType(spec EventSpec) string {
	return d.ID + "." + spec.Suffix
}

// Observes says whether the plugin declares at least one fact.
func (d Descriptor) Observes() bool { return len(d.Events) > 0 }

// EventTypes lists the full types declared by every embedded plugin. It feeds the
// editor, and the validation of a reaction: a type no plugin declares is refused
// when saved rather than staying silent forever.
func EventTypes() []string {
	var types []string
	for _, descriptor := range Registry() {
		for _, spec := range descriptor.Events {
			types = append(types, descriptor.EventType(spec))
		}
	}
	sort.Strings(types)
	return types
}

// ActionRef returns an action's full reference, prefixed by the plugin.
func (d Descriptor) ActionRef(spec ActionSpec) string {
	return d.ID + "." + spec.Suffix
}

// Acts says whether the plugin declares at least one action.
func (d Descriptor) Acts() bool { return len(d.Actions) > 0 }

// FindAction finds the plugin that declares an action reference.
//
// Knowledge of the prefix lives here, next to what applies it: splitting on the
// first dot is a decoding each caller would redo its own way, and `forge.status` —
// an internal action of an earlier version — then looks like an action of a
// `forge` plugin.
//
// It does not consult whether the plugin is enabled: a plan frozen into a run must
// stay readable after the plugin is switched off, and it is the caller's business
// whether what it does with the answer needs an enabled plugin.
func FindAction(ref string) (Descriptor, ActionSpec, bool) {
	for _, descriptor := range Registry() {
		for _, spec := range descriptor.Actions {
			if descriptor.ActionRef(spec) == ref {
				return descriptor, spec, true
			}
		}
	}
	return Descriptor{}, ActionSpec{}, false
}

// Fields returns the fields of one scope.
func (d Descriptor) Fields(scope Scope) []Field {
	if scope == ScopeProject {
		return d.Project
	}
	return d.Global
}

// Scope is the scope of a set of settings.
type Scope string

const (
	ScopeGlobal  Scope = "global"
	ScopeProject Scope = "project"
)

// Values carries a plugin's settings, field by field.
//
// Strings throughout, booleans and lists included. The format matches the
// environment variables it replaces, so a migrated configuration is copied across
// without translation and a new field needs no schema migration.
type Values map[string]string

// Validate refuses settings inconsistent with the descriptor.
//
// `resolved` carries what the wider scope already provides, and required fields
// are checked against that rather than against the typed values alone: a project
// leaving a URL empty because the instance provides it must pass, one leaving it
// empty when nobody provides it must not.
func (d Descriptor) Validate(scope Scope, values Values, resolved Values) error {
	fields := d.Fields(scope)
	declared := make(map[string]Field, len(fields))
	for _, field := range fields {
		declared[field.Name] = field
	}

	var problems []error

	// Unknown fields are refused rather than ignored: an ignored field means a
	// screen saying "saved" over a value nothing will ever read back.
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if _, known := declared[name]; !known {
			problems = append(problems, fmt.Errorf(
				"the plugin %q declares no setting %q at scope %q",
				d.ID, name, scope))
		}
	}

	for _, field := range fields {
		value := strings.TrimSpace(values[field.Name])

		if field.Required {
			effective := value
			if effective == "" {
				effective = strings.TrimSpace(resolved[field.Name])
			}
			if effective == "" {
				problems = append(problems, fmt.Errorf(
					"%s is required", field.Label))
				continue
			}
		}
		if value == "" {
			continue
		}

		switch field.Type {
		case FieldURL:
			parsed, err := url.Parse(value)
			if err != nil || parsed.Scheme == "" || parsed.Host == "" {
				problems = append(problems, fmt.Errorf(
					"%s must be an absolute URL (scheme and host), received %q",
					field.Label, value))
			}
		case FieldBool:
			if value != "true" && value != "false" {
				problems = append(problems, fmt.Errorf(
					"%s is “true” or “false”, received %q", field.Label, value))
			}
		case FieldText, FieldList:
			// Nothing to check: a list with no useful values is still valid input,
			// and constraining the shape of a project identifier would impose a
			// convention that belongs to the deployment.
		default:
			problems = append(problems, fmt.Errorf(
				"%s: unknown field type %q", field.Label, field.Type))
		}
	}

	return errors.Join(problems...)
}

// Resolve layers a project's settings over the instance's.
//
// An empty project value does not erase the instance value; it means "nothing
// particular to say". Erasing would need a separate gesture, and confusing the two
// would lose a URL the first time a form that did not display it was saved.
func Resolve(global, project Values) Values {
	resolved := make(Values, len(global)+len(project))
	maps.Copy(resolved, global)
	for name, value := range project {
		if strings.TrimSpace(value) == "" {
			continue
		}
		resolved[name] = value
	}
	return resolved
}

// SecretFields names the fields of a scope that must be sealed.
func (d Descriptor) SecretFields(scope Scope) []string {
	var secrets []string
	for _, field := range d.Fields(scope) {
		if field.Secret {
			secrets = append(secrets, field.Name)
		}
	}
	return secrets
}

// IsSecret says whether a field of a scope carries a secret value.
func (d Descriptor) IsSecret(scope Scope, name string) bool {
	for _, field := range d.Fields(scope) {
		if field.Name == name {
			return field.Secret
		}
	}
	return false
}

// List splits the value of a [FieldList] field. Here rather than in each caller:
// two divergent splits — one trimming spaces, one not — would read the same
// configuration two ways, and the difference would only show on a status named
// with a trailing space.
func List(value string) []string {
	parts := strings.Split(value, ",")
	items := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			items = append(items, trimmed)
		}
	}
	return items
}
