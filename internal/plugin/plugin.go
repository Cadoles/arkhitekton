// Package plugin resolves the settings plugins declare.
//
// Plugins declare themselves in their own packages (sdk/capability.Plugin), and
// internal/plugins/builtin assembles them. This package reads those declarations: it validates
// typed settings and layers a project's over the instance's.
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

	"forge.cadoles.com/vcarroy/arkhitekton/sdk/capability"
)

// ModuleMSE identifies the MSE plugin: environments, Flux promotion, PortalData
// attestation.
const ModuleMSE = "mse"

// KindLabel names each kind of capability for the interface.
var KindLabel = map[capability.Kind]string{
	capability.KindPipeline: "Pipeline",
	capability.KindTracker:  "Suivi",
	capability.KindForge:    "Forge",
	capability.KindExporter: "Livrables",
	capability.KindModule:   "Module",
}

// Fields returns the fields of one scope.
func Fields(d capability.Plugin, scope Scope) []capability.Field {
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

// Validate refuses settings inconsistent with the plugin's declaration.
//
// `resolved` carries what the wider scope already provides, and required fields
// are checked against that rather than against the typed values alone: a project
// leaving a URL empty because the instance provides it must pass, one leaving it
// empty when nobody provides it must not.
func Validate(d capability.Plugin, scope Scope, values Values, resolved Values) error {
	fields := Fields(d, scope)
	declared := make(map[string]capability.Field, len(fields))
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
		case capability.FieldURL:
			parsed, err := url.Parse(value)
			if err != nil || parsed.Scheme == "" || parsed.Host == "" {
				problems = append(problems, fmt.Errorf(
					"%s must be an absolute URL (scheme and host), received %q",
					field.Label, value))
			}
		case capability.FieldBool:
			if value != "true" && value != "false" {
				problems = append(problems, fmt.Errorf(
					"%s is “true” or “false”, received %q", field.Label, value))
			}
		case capability.FieldText, capability.FieldList:
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
func SecretFields(d capability.Plugin, scope Scope) []string {
	var secrets []string
	for _, field := range Fields(d, scope) {
		if field.Secret {
			secrets = append(secrets, field.Name)
		}
	}
	return secrets
}

// IsSecret says whether a field of a scope carries a secret value.
func IsSecret(d capability.Plugin, scope Scope, name string) bool {
	for _, field := range Fields(d, scope) {
		if field.Name == name {
			return field.Secret
		}
	}
	return false
}

// List splits the value of a [capability.FieldList] field. Here rather than in each caller:
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
