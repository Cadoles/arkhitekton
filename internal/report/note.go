// Package report produces the deliverables attached to a release.
//
// The delivery note is a **contractual document**: it attests to what was delivered, on what date,
// on the strength of which checks. Two consequences for this package.
//
// First, it invents nothing. Every line of the document refers back to a recorded fact — a digest
// read from the registry, a text written by an identified human. What is not known is written "not
// available" and not omitted: a silent absence in a contractual document reads as an assertion.
//
// Second, the delivery note's **layout** is not a technical decision. The exact format must be
// validated with the client before development. This package therefore separates the two strictly:
// [Note] carries the facts and is stable; the PDF rendering is provisional and is replaced without
// touching what feeds it.
package report

import (
	"encoding/json"
	"fmt"
	"time"
)

// Note is the factual content of a delivery note.
//
// A structure serialisable as it is: the JSON rendering is an export in its own right, not an
// intermediate. It is even the only format whose content depends on no presentation decision,
// hence the only deliverable one can rely on before the paper format is validated.
type Note struct {
	// Release is the tag delivered, the document's identity.
	Release string `json:"release"`
	// Version is the name of the matching Redmine version. Equal to the tag's base by construction,
	// repeated because it is under that name that the functional scope is discussed.
	Version string `json:"version"`
	Kind    string `json:"kind"`
	// ImageVersion is the version the image carries. Distinct from the tag for any final release
	// produced by re-tag, and it is **that** which will be observed on the environments.
	ImageVersion string `json:"image_version"`
	SourceTag    string `json:"source_release,omitempty"`
	GitRef       string `json:"git_ref,omitempty"`

	Images []Image `json:"images"`

	ManualActions *ManualActions `json:"manual_actions,omitempty"`

	// Unavailable lists what the document cannot attest to, and why. Written rather than omitted: in a
	// contractual document, a missing section reads as an assertion.
	Unavailable []string `json:"unavailable,omitempty"`

	GeneratedAt time.Time `json:"generated_at"`
	GeneratedBy string    `json:"generated_by"`
	// Provisional says that the layout awaits the client's validation. Carried by the data and not by
	// the rendering alone: the JSON must say it too, otherwise an integrator could treat it as final.
	Provisional bool `json:"provisional"`
}

// Image is a tag and its digest.
type Image struct {
	Repository string `json:"repository"`
	Tag        string `json:"tag"`
	// An empty Digest means the tag could not be checked in the registry. The document must say so: a
	// digest is what proves that the image delivered is the one that was validated.
	Digest string `json:"digest,omitempty"`
}

// ManualActions is the account of the delivery's manual acts.
type ManualActions struct {
	Markdown  string     `json:"markdown"`
	UpdatedBy string     `json:"updated_by,omitempty"`
	UpdatedAt *time.Time `json:"updated_at,omitempty"`
}

// KnownUnavailable names what will be missing from the document until the matching work is
// delivered.
//
// A constant rather than a text slipped into the rendering: the list must be the same in the JSON
// and in the PDF, and disappear in a single act the day the data exists.
var KnownUnavailable = []string{
	"Résultats de tests : la collecte des résultats n'est pas encore en place.",
	"Verdict de sécurité : la normalisation des findings n'est pas encore en place.",
	"Anomalies connues : la synchronisation Redmine n'est pas encore en place.",
}

// Validate refuses a delivery note that attests to nothing.
//
// The check is deliberately minimal — the document must be producible early, precisely so that it
// can be reviewed — but it refuses the absence of identity: a delivery note with neither release
// nor version names no delivery.
func (n Note) Validate() error {
	if n.Release == "" {
		return fmt.Errorf("delivery note with no release")
	}
	if n.ImageVersion == "" {
		return fmt.Errorf("delivery note of %q with no image version: nothing identifies what is delivered", n.Release)
	}
	return nil
}

// UnverifiedImages returns the tags whose digest could not be read.
//
// Used by both renderings: the document must name what it could not check, in the place where the
// reader looks for the proof.
func (n Note) UnverifiedImages() []string {
	var unverified []string
	for _, image := range n.Images {
		if image.Digest == "" {
			unverified = append(unverified, image.Tag)
		}
	}
	return unverified
}

// RenderJSON renders the delivery note as JSON.
//
// Indented: this file is read by humans as much as by programs, and it may be attached to a
// contractual exchange.
func RenderJSON(note Note) ([]byte, error) {
	if err := note.Validate(); err != nil {
		return nil, err
	}
	return json.MarshalIndent(note, "", "  ")
}
