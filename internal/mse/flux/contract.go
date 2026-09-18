package flux

/*
 * What the deployer exchanges with the plugin.
 *
 * These types formed the `provider.Deployer` boundary, written so that another deployment tool
 * could replace Flux. They had a single implementation, and their only caller is the MSE plugin:
 * they now live beside the deployer that uses them.
 */

// Target is an environment, seen from the deployer.
type Target struct {
	ID string
	// Observable says whether this environment can be queried. False for production, in its
	// independent cluster. It is a state distinct from "unknown": without the distinction, a frozen
	// card looks like an outage.
	Observable bool
	// Settings carries the settings declared in the catalogue for this environment:
	// [SettingSuffix], [SettingAcceptsPrerelease], [SettingKustomization].
	Settings map[string]string
}

// Setting returns a setting, or the default value supplied.
func (t Target) Setting(key, fallback string) string {
	if value, found := t.Settings[key]; found && value != "" {
		return value
	}
	return fallback
}

// Deployment is a promotion request.
type Deployment struct {
	Target Target
	// Version is the release's identity — its tag. It is what decides eligibility for an
	// environment.
	Version string
	// Artifact is what the image really exists under. Distinct from Version, and the gap is the
	// central trap: a final release produced by re-tag carries the tag `1.8.0` although its image
	// exists under `1.8.0-rc3`.
	Artifact string
	// Name is the short image repository to promote. A release publishes several, and the deployer
	// is queried once per image.
	Name string
}

// Plan describes the routing to perform: a pipeline and its parameters.
type Plan struct {
	// Kind is the pipeline kind that performs the routing.
	Kind string
	// Params are its parameters.
	Params map[string]string
	// Reference is the tag set on the image, for the promotion history.
	Reference string
}

// Resolution says where what is asserted about an environment comes from.
//
// Attesting that a version is running is not the same assertion as observing the tag Flux kept.
// Merging them would announce as "delivered" an environment whose migrations failed. The values
// are the ones written in the database and returned by the interface.
type Resolution string

const (
	// ResolutionUnobservable: nothing can be asserted.
	ResolutionUnobservable Resolution = "unobservable"
	// ResolutionDeclared: Flux says what it kept. An intention, not an observation — the historical
	// value is `git`.
	ResolutionDeclared Resolution = "git"
	// ResolutionAttested: the application reports its version, and that its migrations completed.
	// The only level that allows "delivered".
	ResolutionAttested Resolution = "portaldata"
)

// Observation is what Flux knows about an environment.
type Observation struct {
	// Version is what Flux kept. Empty when it does not know.
	Version string
	// State is the state reported, taken as it is for display.
	State string
	// Resolution says what the preceding is worth.
	Resolution Resolution
}
