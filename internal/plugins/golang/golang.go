// Package golang brings a Go module's pipelines: its binaries, its unit tests, its static analysis
// and its known vulnerabilities. The package is `golang`, the plugin's identifier `go`.
//
// Each pipeline clones, then downloads the modules in a task of its own, the only one that holds
// the credentials: the forge's private modules are fetched over SSH with the clone's key. The tasks
// that follow find them in the workspace.
package golang

import (
	"embed"

	"forge.cadoles.com/vcarroy/arkhitekton/sdk"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/capability"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/parse"
)

// ID is the plugin's identifier.
const ID = "go"

//go:embed manifests
var manifests embed.FS

//go:embed readings
var readings embed.FS

func reading(name string) []byte {
	declared, err := readings.ReadFile("readings/" + name)
	if err != nil {
		// The embedding is fixed at compile time; the registry's tests read every one.
		panic(err)
	}
	return declared
}

// Manifests are the Tekton definitions the plugin ships.
func Manifests() []capability.Manifest {
	loaded, err := capability.LoadManifests(manifests, "manifests")
	if err != nil {
		// The embedding is fixed at compile time; a test reads it.
		panic(err)
	}
	return loaded
}

// Plugin describes the Go plugin.
func Plugin() capability.Plugin {
	all := Manifests()
	return capability.Plugin{
		ID:         ID,
		Version:    "1.0.0",
		APIVersion: sdk.APIVersion,
		Label:      "Go",
		Summary:    "Binaires, tests unitaires (go test), analyse statique (golangci-lint) et vulnérabilités (govulncheck) d'un module Go.",
		Pipelines: []capability.Pipeline{
			build(pick(all, "go-build", "go-build-binaries", "go-modules")),
			unitTests(pick(all, "go-unit-tests", "go-test", "go-modules")),
			staticTests(pick(all, "go-static-tests", "golangci-lint", "go-modules")),
			vulncheck(pick(all, "go-vulncheck", "govulncheck", "go-modules")),
		},
	}
}

// The Tasks the pipelines use without shipping them: the core's deployment ships both.
var external = []string{"git-clone", "publish-artifact"}

func source(extra map[string]capability.Param) map[string]capability.Param {
	params := map[string]capability.Param{
		"repo-url": {Source: capability.ParamBinding, DefaultKey: "project.repoUrl"},
		"revision": {Source: capability.ParamInput, Required: true, Suggest: "git-ref"},
		"private-modules": {
			Source: capability.ParamInput, Label: "Modules privés",
			Help:        "Les motifs de GOPRIVATE, séparés par des virgules. Vide : l'hôte de la forge du projet.",
			Placeholder: "forge.example.fr",
		},
	}
	for name, param := range extra {
		params[name] = param
	}
	return params
}

func workspaces(storage string) []capability.Workspace {
	return []capability.Workspace{
		{Name: "git-credentials", Need: capability.NeedSourceRead},
		{Name: "shared-data", Volume: &capability.Volume{Storage: storage}},
	}
}

var needs = []capability.Need{{Name: capability.NeedSourceRead,
	Reason: "clone the repository, and the forge's private modules it depends on"}}

func results(object string, members ...capability.Member) []capability.Artifact {
	return []capability.Artifact{{
		Name: "results", Param: "results-upload-url", Object: object,
		Format: capability.FormatArchive, Members: members,
	}}
}

func build(manifests []capability.Manifest) capability.Pipeline {
	return capability.Pipeline{
		Name:        "build",
		Label:       "Binaires Go",
		Interpreter: binaries{},
		Spec: capability.PipelineSpec{
			PipelineRef:   "go-build",
			Group:         "build",
			Manifests:     manifests,
			ExternalTasks: external,
			Params: source(map[string]capability.Param{
				"packages": {
					Source: capability.ParamInput, Required: true, Label: "Paquets",
					Help:        "Les paquets main à construire, séparés par des espaces.",
					Placeholder: "./cmd/app",
				},
				"platforms": {
					Source: capability.ParamInput, Label: "Plateformes",
					Help:        "os/arch, séparés par des virgules. Vide : linux/amd64 et linux/arm64.",
					Placeholder: "linux/amd64,linux/arm64",
				},
				"version": {Source: capability.ParamInput, Required: true, Suggest: "release-tag"},
				"ldflags": {
					Source: capability.ParamInput, Label: "ldflags",
					Help:        "`{version}` y est remplacé par la version de la release.",
					Placeholder: "-X main.version={version}",
				},
			}),
			Workspaces: workspaces("4Gi"),
			Needs:      needs,
			Produces:   []capability.Product{capability.ProductBinary},
			Artifacts: []capability.Artifact{{
				Name: "binaries", Param: "binaries-upload-url", Object: "binaries.tar.gz",
				Format: capability.FormatArchive,
				Members: []capability.Member{
					{Match: "binaries/*.tar.gz", Parser: parse.Attach, Tool: "go-build", MediaType: "application/gzip"},
					{Match: "binaries/SHA256SUMS", Parser: parse.Attach, Tool: "go-build", MediaType: "text/plain"},
				},
			}},
		},
	}
}

func unitTests(manifests []capability.Manifest) capability.Pipeline {
	return capability.Pipeline{
		Name:    "unit-tests",
		Label:   "Tests unitaires Go",
		Reading: reading("unit-tests.yaml"),
		Spec: capability.PipelineSpec{
			PipelineRef:   "go-unit-tests",
			Group:         "unit",
			Manifests:     manifests,
			ExternalTasks: external,
			Params: source(map[string]capability.Param{
				"packages": {Source: capability.ParamInput, Label: "Paquets", Placeholder: "./..."},
				"flags": {
					Source: capability.ParamInput, Label: "Options de go test",
					Placeholder: "-race -count=1",
				},
			}),
			Workspaces:   workspaces("2Gi"),
			Needs:        needs,
			Produces:     []capability.Product{capability.ProductResults},
			Verification: true,
			Artifacts: results("unit-tests.tar.gz",
				capability.Member{Match: "go-test/*.json", Parser: parse.GoTestJSON, Tool: "go-test"}),
		},
	}
}

func staticTests(manifests []capability.Manifest) capability.Pipeline {
	return capability.Pipeline{
		Name:    "static-tests",
		Label:   "Analyse statique Go",
		Reading: reading("static-tests.yaml"),
		Spec: capability.PipelineSpec{
			PipelineRef:   "go-static-tests",
			Group:         "static",
			Manifests:     manifests,
			ExternalTasks: external,
			Params:        source(nil),
			Workspaces:    workspaces("2Gi"),
			Needs:         needs,
			Produces:      []capability.Product{capability.ProductResults},
			Verification:  true,
			Artifacts: results("static-tests.tar.gz",
				capability.Member{Match: "golangci-lint/*.xml", Parser: parse.JUnitXML, Tool: "golangci-lint"}),
		},
	}
}

func vulncheck(manifests []capability.Manifest) capability.Pipeline {
	return capability.Pipeline{
		Name:        "vulncheck",
		Label:       "Vulnérabilités Go",
		Interpreter: vulnerabilities{},
		Spec: capability.PipelineSpec{
			PipelineRef:   "go-vulncheck",
			Group:         "security",
			Manifests:     manifests,
			ExternalTasks: external,
			Params: source(map[string]capability.Param{
				"packages": {Source: capability.ParamInput, Label: "Paquets", Placeholder: "./..."},
			}),
			Workspaces:   workspaces("2Gi"),
			Needs:        needs,
			Produces:     []capability.Product{capability.ProductResults},
			Verification: true,
			Artifacts: results("vulncheck.tar.gz",
				capability.Member{Match: "govulncheck/*.json", Parser: parse.Raw, Tool: "govulncheck"}),
		},
	}
}

// pick keeps the named manifests, in the order given, each once.
func pick(all []capability.Manifest, names ...string) []capability.Manifest {
	var kept []capability.Manifest
	seen := map[string]bool{}
	for _, name := range names {
		for _, manifest := range all {
			if manifest.Name == name && !seen[manifest.Kind+"/"+name] {
				seen[manifest.Kind+"/"+name] = true
				kept = append(kept, manifest)
			}
		}
	}
	return kept
}
