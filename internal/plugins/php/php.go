// Package php brings the static analysis and the unit tests of a PHP project.
//
// The Tasks write their reports under `test-results/<tool>/` and the pipeline publishes the whole
// directory as one archive, in `finally`: a failing tool must not cost the reports of the others.
package php

import (
	"embed"

	"forge.cadoles.com/vcarroy/arkhitekton/sdk"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/capability"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/parse"
)

// ID is the plugin's identifier.
const ID = "php"

//go:embed manifests
var manifests embed.FS

// Manifests are the Tekton definitions the plugin ships.
func Manifests() []capability.Manifest {
	loaded, err := capability.LoadManifests(manifests, "manifests")
	if err != nil {
		// The embedding is fixed at compile time; a test reads it.
		panic(err)
	}
	return loaded
}

// Plugin describes the PHP plugin.
func Plugin() capability.Plugin {
	all := Manifests()
	return capability.Plugin{
		ID:         ID,
		Version:    "1.0.0",
		APIVersion: sdk.APIVersion,
		Label:      "PHP",
		Summary:    "Analyse statique (PHPStan, PHP-CS-Fixer) et tests unitaires PHPUnit.",
		Pipelines: []capability.Pipeline{
			staticTests(pick(all, "php-static-tests", "phpstan", "cs-fixer")),
			unitTests(pick(all, "php-unit-tests", "unit-test")),
		},
	}
}

// The Tasks the pipelines use without shipping them.
var external = []string{"git-clone", "composer-install", "publish-artifact"}

func source() map[string]capability.Param {
	return map[string]capability.Param{
		"repo-url": {Source: capability.ParamBinding, DefaultKey: "project.repoUrl"},
		"revision": {Source: capability.ParamInput, Required: true, Suggest: "git-ref"},
	}
}

func workspaces() []capability.Workspace {
	return []capability.Workspace{
		{Name: "git-credentials", Need: capability.NeedSourceRead},
		{Name: "shared-data", Volume: &capability.Volume{Storage: "1Gi"}},
	}
}

var needs = []capability.Need{{Name: capability.NeedSourceRead, Reason: "clone the repository to test"}}

func staticTests(manifests []capability.Manifest) capability.Pipeline {
	return capability.Pipeline{
		Name:        "static-tests",
		Label:       "Analyse statique",
		Interpreter: staticReader{},
		Spec: capability.PipelineSpec{
			PipelineRef:   "php-static-tests",
			Group:         "static",
			Manifests:     manifests,
			ExternalTasks: external,
			Params:        source(),
			Workspaces:    workspaces(),
			Needs:         needs,
			Produces:      []capability.Product{capability.ProductResults},
			Verification:  true,
			Artifacts: []capability.Artifact{{
				Name: "results", Param: "results-upload-url", Object: "static-tests.tar.gz",
				Format: capability.FormatArchive,
				Members: []capability.Member{
					{Match: "phpstan/*.json", Parser: parse.PHPStanJSON, Tool: "phpstan"},
					{Match: "cs-fixer/*.json", Parser: parse.CSFixerJSON, Tool: "cs-fixer"},
				},
			}},
		},
	}
}

func unitTests(manifests []capability.Manifest) capability.Pipeline {
	return capability.Pipeline{
		Name:        "unit-tests",
		Label:       "Tests unitaires",
		Interpreter: unitReader{},
		Spec: capability.PipelineSpec{
			PipelineRef:   "php-unit-tests",
			Group:         "unit",
			Manifests:     manifests,
			ExternalTasks: external,
			Params:        source(),
			Workspaces:    workspaces(),
			Needs:         needs,
			Produces:      []capability.Product{capability.ProductResults},
			Verification:  true,
			Artifacts: []capability.Artifact{{
				Name: "results", Param: "results-upload-url", Object: "unit-tests.tar.gz",
				Format: capability.FormatArchive,
				Members: []capability.Member{
					{Match: "unit/*.xml", Parser: parse.JUnitXML, Tool: "phpunit"},
				},
			}},
		},
	}
}

// pick keeps the named manifests, in the order given.
func pick(all []capability.Manifest, names ...string) []capability.Manifest {
	var kept []capability.Manifest
	for _, name := range names {
		for _, manifest := range all {
			if manifest.Name == name {
				kept = append(kept, manifest)
			}
		}
	}
	return kept
}
