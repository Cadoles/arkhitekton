package store

import "testing"

// A release promotes the images its latest run published: not the `tag` result, not the
// development image, not what a first run built before a rebuild.
func TestAReleasePromotesWhatItsLatestBuildPublished(t *testing.T) {
	artifacts := []PlanArtifact{
		{RunID: 9, Name: "image", Kind: "image", Reference: "reg/team/portal:1.2.0-rc.1", Promoted: true},
		{RunID: 9, Name: "tag", Kind: "image", Reference: "1.2.0-rc.1", Promoted: true},
		{RunID: 9, Name: "image", Kind: "image", Reference: "reg/team/portal-dev:1.2.0-rc.1", Promoted: false},
		{RunID: 9, Name: "results", Kind: "results", Reference: "s3://x", Promoted: true},
		{RunID: 4, Name: "image", Kind: "image", Reference: "reg/team/old:1.2.0-rc.1", Promoted: true},
	}
	images := PromotedImages(artifacts)
	if len(images) != 1 || images[0].Name != "portal" || images[0].Reference != "reg/team/portal:1.2.0-rc.1" {
		t.Errorf("images = %+v", images)
	}
}
