// Package mse brings the pipelines that test a deployed MSE: the integration tests, which deploy
// the release into a namespace of their own, and the end-to-end campaign.
package mse

import (
	"embed"

	"forge.cadoles.com/vcarroy/arkhitekton/sdk"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/capability"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/parse"
)

// ID is the plugin's identifier. The same as the MSE module's: the two halves of one plugin, until
// the module's screens and loops move onto capabilities too.
const ID = "mse"

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

// Plugin describes the MSE plugin's pipelines.
func Plugin() capability.Plugin {
	return capability.Plugin{ID: ID, Version: "1.0.0", APIVersion: sdk.APIVersion, Pipelines: []capability.Pipeline{
		integrationTests(Manifests()),
		e2eTests(),
	}}
}

func integrationTests(manifests []capability.Manifest) capability.Pipeline {
	binding := func(key string) capability.Param {
		return capability.Param{Source: capability.ParamBinding, DefaultKey: key}
	}
	return capability.Pipeline{
		Name:        "integration-tests",
		Label:       "Tests d'intégration",
		Interpreter: integrationReader{},
		Spec: capability.PipelineSpec{
			PipelineRef:   "php-integration-test",
			Group:         "integration",
			Manifests:     manifests,
			ExternalTasks: []string{"git-clone", "deploy-mse", "publish-artifact"},
			Exclusive:     true,
			Verification:  true,
			Produces:      []capability.Product{capability.ProductResults},
			Params: map[string]capability.Param{
				"mse-repo":            binding("project.repoUrl"),
				"mse-kustom-repo":     binding("forge.mseKustom.repoUrl"),
				"mse-kustom-revision": binding("forge.mseKustom.revision"),
				"overlay-location":    binding("mse.overlayLocation"),
				"reg-project":         binding("registry.project"),
				"minio-host":          binding("minio.host"),
				"minio-bucket":        binding("minio.pipelineBucket"),
				"image-tag":           {Source: capability.ParamInput, Required: true, Suggest: "release-tag"},
				"mse-revision":        {Source: capability.ParamInput, Required: true, Suggest: "git-ref"},
			},
			Workspaces: []capability.Workspace{
				{Name: "git-credentials", Need: capability.NeedSourceRead},
				{Name: "dockerconfig", Need: capability.NeedRegistryPull},
				{Name: "mse", Volume: &capability.Volume{Storage: "1Gi"}},
				{Name: "kustomization", Volume: &capability.Volume{Storage: "1Gi"}},
			},
			Needs: []capability.Need{
				{Name: capability.NeedSourceRead, Reason: "clone the application and its kustomization"},
				{Name: capability.NeedRegistryPull, Reason: "pull the release's images"},
				{Name: capability.NeedClusterDeploy, Reason: "deploy the release into a test namespace"},
			},
			Artifacts: []capability.Artifact{{
				Name: "results", Param: "results-upload-url", Object: "integration-tests.tar.gz",
				Format: capability.FormatArchive,
				Members: []capability.Member{
					{Match: "integration/*.xml", Parser: parse.JUnitXML, Tool: "phpunit-integration"},
				},
			}},
		},
	}
}

// e2eTests runs the campaign of the MSE end-to-end repository. Its Pipeline is provided by the
// operator.
func e2eTests() capability.Pipeline {
	archive := func(name, param, object string, members ...capability.Member) capability.Artifact {
		return capability.Artifact{
			Name: name, Param: param, Object: object, Format: capability.FormatArchive, Members: members,
		}
	}
	return capability.Pipeline{
		Name:        "e2e-tests",
		Label:       "Tests E2E",
		Interpreter: e2eReader{},
		Spec: capability.PipelineSpec{
			PipelineRef: "e2e-test",
			Group:       "e2e",
			Exclusive:   true,
			Produces:    []capability.Product{capability.ProductResults},
			Params: map[string]capability.Param{
				"repo-url":    {Source: capability.ParamBinding, DefaultKey: "forge.e2e.repoUrl"},
				"revision":    {Source: capability.ParamInput, Required: true, Suggest: "git-ref"},
				"environment": {Source: capability.ParamInput, Required: true, Suggest: "environment"},
			},
			Workspaces: []capability.Workspace{
				{Name: "git-credentials", Need: capability.NeedSourceRead},
				{Name: "shared-data", Volume: &capability.Volume{Storage: "1Gi"}},
			},
			Needs: []capability.Need{
				{Name: capability.NeedSourceRead, Reason: "clone the end-to-end repository"},
			},
			Artifacts: []capability.Artifact{
				archive("reports", "reports-upload-url", "e2e-reports.tar.gz",
					capability.Member{Match: "e2e/*.json", Parser: parse.MochawesomeJSON, Tool: "cypress"},
					capability.Member{Match: "*/*mocha*.json", Parser: parse.MochawesomeJSON, Tool: "cypress"}),
				archive("videos", "videos-upload-url", "e2e-videos.tar.gz"),
				archive("screenshots", "screenshots-upload-url", "e2e-screenshots.tar.gz"),
			},
		},
	}
}
