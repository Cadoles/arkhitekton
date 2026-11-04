package http

import (
	"context"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/results"
)

// binaryPayload is a file a release's build kept: a binary, its checksums.
type binaryPayload struct {
	// ID names it in the attachments' route, `…/releases/{tag}/attachments/{id}`.
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	SizeBytes int64  `json:"size_bytes"`
	MediaType string `json:"media_type,omitempty"`
}

// releaseBinaries lists what the release's binary builds kept: the attachments of its results in
// the `build` group, the latest build's only. A read failure leaves the list empty rather than the
// page unreadable.
func (h *ReleasesHandler) releaseBinaries(ctx context.Context, releaseID int64) []binaryPayload {
	reports, err := h.store.ListReleaseTestResults(ctx, releaseID)
	if err != nil {
		h.logger.Error("binaries of a release", "release", releaseID, "error", err)
		return nil
	}
	var binaries []binaryPayload
	seen := map[string]bool{}
	for _, report := range reports {
		if report.Group != string(results.GroupBuild) {
			continue
		}
		kept, err := h.store.ResultAttachments(ctx, report.ID)
		if err != nil {
			h.logger.Error("binaries of a release", "release", releaseID, "error", err)
			continue
		}
		for _, attachment := range kept {
			// The most recent first: a rebuild's files replace the earlier ones of the same name.
			if seen[attachment.Name] {
				continue
			}
			seen[attachment.Name] = true
			binaries = append(binaries, binaryPayload{
				ID: attachment.ID, Name: attachment.Name, SizeBytes: attachment.SizeBytes,
				MediaType: attachment.MediaType,
			})
		}
	}
	return binaries
}
