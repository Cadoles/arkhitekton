package catalog

import (
	"context"
	"strings"
	"testing"
)

// A typo in `uploads[].param` would only surface at the first run, rejected by
// Tekton as an unknown parameter — that is, after waiting for a job to be
// scheduled.
func TestValidateRejectsAnUploadParamThePipelineIgnores(t *testing.T) {
	pipelines := &Catalog{Pipelines: []Pipeline{{
		Kind: "static-tests", PipelineRef: "php-static-tests",
		Uploads: map[string]Upload{
			"results": {Param: "result-upload-url", Artifact: "static-tests.tar.gz"},
		},
	}}}

	err := pipelines.ValidateAgainst(context.Background(), fakeCluster{pipelines: map[string]PipelineSpec{
		"php-static-tests": {Name: "php-static-tests", Params: map[string]bool{
			"results-upload-url": true,
		}},
	}})
	if err == nil {
		t.Fatal("an unknown upload parameter must prevent start-up")
	}
	if !strings.Contains(err.Error(), "result-upload-url") {
		t.Errorf("error = %v, expected the offending parameter's name", err)
	}
}

// The upload parameter is absent from `params`: it is minted at launch. Counting
// it as missing would refuse a correct catalogue.
func TestAnUploadParamCountsAsProvided(t *testing.T) {
	pipelines := &Catalog{Pipelines: []Pipeline{{
		Kind: "static-tests", PipelineRef: "php-static-tests",
		Uploads: map[string]Upload{
			"results": {Param: "results-upload-url", Artifact: "static-tests.tar.gz"},
		},
	}}}

	err := pipelines.ValidateAgainst(context.Background(), fakeCluster{pipelines: map[string]PipelineSpec{
		"php-static-tests": {Name: "php-static-tests", Params: map[string]bool{
			// No default: the Pipeline requires it.
			"results-upload-url": false,
		}},
	}})
	if err != nil {
		t.Errorf("error = %v, expected none", err)
	}
}

func TestAnIncompleteUploadIsRejected(t *testing.T) {
	pipelines := &Catalog{Pipelines: []Pipeline{{
		Kind: "static-tests", PipelineRef: "php-static-tests",
		Uploads: map[string]Upload{"results": {Param: "results-upload-url"}},
	}}}

	err := pipelines.ValidateAgainst(context.Background(), fakeCluster{pipelines: map[string]PipelineSpec{
		"php-static-tests": {Name: "php-static-tests", Params: map[string]bool{
			"results-upload-url": true,
		}},
	}})
	if err == nil {
		t.Error("an artifact with no object name makes its key uncomputable")
	}
}
