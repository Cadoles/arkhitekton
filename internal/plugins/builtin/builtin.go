// Package builtin assembles the plugins compiled into the binary.
//
// The one place that lists them. The binary and the tests of the shipped configuration both need
// the same list; a second copy would let them disagree about what a deployment runs.
package builtin

import (
	"github.com/Cadoles/arkhitekton/internal/capabilities"
	"github.com/Cadoles/arkhitekton/internal/plugins/docker"
	"github.com/Cadoles/arkhitekton/internal/plugins/gitea"
	"github.com/Cadoles/arkhitekton/internal/plugins/golang"
	"github.com/Cadoles/arkhitekton/internal/plugins/k6"
	"github.com/Cadoles/arkhitekton/internal/plugins/php"
	"github.com/Cadoles/arkhitekton/internal/plugins/redmine"
	"github.com/Cadoles/arkhitekton/internal/plugins/review"
	"github.com/Cadoles/arkhitekton/internal/plugins/rocketchat"
	"github.com/Cadoles/arkhitekton/sdk/capability"
)

// Plugins are the plugins compiled into the binary.
func Plugins() []capability.Plugin {
	return []capability.Plugin{
		docker.Plugin(), php.Plugin(), golang.Plugin(), k6.Plugin(), review.Plugin(),
		redmine.Plugin(), gitea.Plugin(), rocketchat.Plugin(),
	}
}

// Registry gathers what the built-in plugins bring.
func Registry() (*capabilities.Registry, error) {
	return capabilities.New(Plugins()...)
}

// Stacks are the language plugins: the automation catch-up chains their kinds only for the
// language a project was built with before it declared its automation.
func Stacks() []string {
	return []string{php.ID, golang.ID}
}
