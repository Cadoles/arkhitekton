// Package security brings the security audit: every scanner run on the source and the images,
// merged, deduplicated and filtered by exceptions into one report by the pipeline itself.
//
// The dashboard reads that report as it is. Its Pipeline is provided by the operator.
package security

import (
	"forge.cadoles.com/vcarroy/arkhitekton/internal/capability"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/parse"
	report "forge.cadoles.com/vcarroy/arkhitekton/internal/parse/security"
)

// ID is the plugin's identifier.
const ID = "security"

// Plugin describes the security plugin.
func Plugin() capability.Plugin {
	return capability.Plugin{ID: ID, Version: "1.0.0", Pipelines: []capability.Pipeline{audit()}}
}

func audit() capability.Pipeline {
	binding := func(key string) capability.Param {
		return capability.Param{Source: capability.ParamBinding, DefaultKey: key}
	}
	file := func(name, param, object string, parser parse.ID) capability.Artifact {
		return capability.Artifact{
			Name: name, Param: param, Object: object, Format: capability.FormatFile,
			Members: []capability.Member{{Match: object, Parser: parser, Tool: "security-audit"}},
		}
	}
	return capability.Pipeline{
		Name:  "security-audit",
		Label: "Audit de sécurité",
		Spec: capability.PipelineSpec{
			PipelineRef:  "security-audit",
			Group:        "security",
			Verification: true,
			Produces:     []capability.Product{capability.ProductResults},
			Params: map[string]capability.Param{
				"app-repo-url":        binding("project.repoUrl"),
				"app-revision":        {Source: capability.ParamInput, Required: true, Suggest: "git-ref"},
				"mse-kustom-repo-url": binding("forge.mseKustom.repoUrl"),
				"mse-kustom-revision": binding("forge.mseKustom.revision"),
				"tekton-repo-url":     binding("forge.tekton.repoUrl"),
				"portal-image": {
					Source: capability.ParamInput, Required: true, Suggest: "image", Image: "portal",
				},
				"consumer-image": {
					Source: capability.ParamInput, Required: true, Suggest: "image", Image: "portal-cli",
				},
				"minio-host":   binding("minio.host"),
				"minio-bucket": binding("minio.pipelineBucket"),
			},
			Workspaces: []capability.Workspace{
				{Name: "git-credentials", Need: capability.NeedSourceRead},
				{Name: "docker-config", Need: capability.NeedRegistryPull},
				{Name: "shared", Volume: &capability.Volume{Storage: "2Gi"}},
			},
			Needs: []capability.Need{
				{Name: capability.NeedSourceRead, Reason: "scan the source"},
				{Name: capability.NeedRegistryPull, Reason: "scan the images"},
			},
			Artifacts: []capability.Artifact{
				file("summary", "summary-upload-url", report.SummaryFileName, parse.SecuritySummaryJSON),
				file("findings", "findings-upload-url", report.FindingsFileName, parse.SecurityFindingsJSONL),
			},
		},
	}
}
