// Package images composes the references of published images.
//
// Nothing more: the names of the published repositories used to live here as
// constants, along with the lists of what a release builds and what a re-tag
// propagates. They described one repository rather than the product, and keeping
// them in Go meant recompiling the dashboard to publish one more image.
//
// They now live in the `projects` table, which already describes what each
// repository builds: registry, Dockerfile stages, image repositories fed, and which
// are promoted. What remains here is the only thing that is neither data nor
// decision: how to write `registry/name:tag`.
//
// The registry prefix stays a parameter: it differs from one deployment to the
// next.
package images

// Repository composes the full repository, without a tag.
//
// Empty when the project is: better to show nothing than a truncated name that
// would look like a pullable reference without being one.
func Repository(project, name string) string {
	if project == "" {
		return ""
	}
	return project + "/" + name
}

// Reference composes the full reference of a published image.
func Reference(project, name, tag string) string {
	repository := Repository(project, name)
	if repository == "" || tag == "" {
		return ""
	}
	return repository + ":" + tag
}
