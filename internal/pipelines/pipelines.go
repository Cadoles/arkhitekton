// Package pipelines puts in place the Tekton definitions the plugins ship.
//
// The **core** — build, re-tag — lives in the dashboard's `deploy/` and arrives with it, like the
// migration Job. What a plugin ships (PHP's tests, Go's binaries) is installed with the plugin's
// capabilities, and withdrawn with it.
//
// # What the dashboard owns, and what it refuses
//
// It installs only resources it **names**, marked `managed-by`, and **never adopts** what already
// exists. It is the same rule as for the deploy-key secrets, and it has a happy consequence here:
// on a cluster where `mse-tekton` has already installed its pipelines, the dashboard observes and
// withdraws, instead of overwriting definitions someone else maintains.
//
// This mechanism therefore serves **autonomy**: an instance that needed no pre-existing pipeline
// repository.
package pipelines

// Resource is a Tekton object to install, as a plugin ships it.
type Resource struct {
	// Name is the object's name, read from its manifest. Serves to say what was installed, and what
	// was refused.
	Name string
	// Kind is `Pipeline` or `Task`.
	Kind string
	// Manifest is the YAML as it is. Not rewritten: the dashboard installs what the plugin declares,
	// and a manifest modified on the fly would be a definition nobody has reviewed.
	Manifest []byte
	// Plugin is the plugin that ships it, set on the object as a label: what lets the dashboard
	// withdraw a plugin's definitions once it is uninstalled.
	Plugin string
}
