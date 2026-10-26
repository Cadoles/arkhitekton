package http

import (
	"context"
	"errors"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/blob"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
)

/*
 * Serving the files a run kept: screenshots, videos, reports.
 *
 * They come from pipelines, and a pipeline runs whatever its repository says: an HTML report is
 * third-party markup served under the dashboard's origin, where the session cookie is and where the
 * CSRF token can be read by script. Hence, on every response, whatever the type:
 *
 *   - `Content-Security-Policy: sandbox` without `allow-scripts` nor `allow-same-origin`: the
 *     document runs in an opaque origin, with no script, and loads nothing but its own images and
 *     media;
 *   - `nosniff`, so a file is never read as another type than the one served;
 *   - an inline display only for types a browser renders without running anything of the file's:
 *     images other than SVG, videos, text and HTML (under the sandbox above). Everything else,
 *     SVG included, is a download.
 *
 * No presigned URL: like the deliverables, a file is read through the API, under the reader's
 * role.
 */

// AttachmentObjects opens a stored file at any offset: a video is sought into.
type AttachmentObjects interface {
	Open(ctx context.Context, key string) (blob.Opened, blob.ObjectInfo, error)
}

// attachmentWriteTimeout replaces the server's write deadline for a file: a video takes longer to
// send than an API answer.
const attachmentWriteTimeout = 30 * time.Minute

// attachmentPolicy applies to every file served, whatever its type.
const attachmentPolicy = "sandbox; default-src 'none'; img-src 'self' data:; media-src 'self'; " +
	"style-src 'unsafe-inline'; frame-ancestors 'self'"

// inlineTypes are shown in the browser; any other type is downloaded.
var inlineTypes = map[string]bool{
	"image/png": true, "image/jpeg": true, "image/gif": true, "image/webp": true,
	"video/mp4": true, "video/webm": true,
	"text/plain": true, "text/html": true, "application/json": true,
}

// WithAttachments wires the reading of the files results keep.
func (h *TestsHandler) WithAttachments(objects AttachmentObjects) *TestsHandler {
	h.attachments = objects
	return h
}

// Attachment serves one file of a release's results.
func (h *TestsHandler) Attachment(w http.ResponseWriter, r *http.Request) {
	currentID, ok := requestProject(w, r, h.store)
	if !ok {
		return
	}
	release, err := h.store.GetRelease(r.Context(), currentID, r.PathValue("tag"))
	if err != nil {
		if errors.Is(err, store.ErrReleaseNotFound) {
			writeProblem(w, http.StatusNotFound, "Release not found", "")
			return
		}
		writeProblem(w, http.StatusInternalServerError, "Cannot read the release", "")
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeProblem(w, http.StatusBadRequest, "Invalid attachment identifier", "")
		return
	}

	attachment, err := h.store.GetAttachment(r.Context(), id)
	// A file of another release answers like a missing one: identifiers are sequential, and telling
	// them apart would let anyone enumerate the files of releases they cannot read.
	if errors.Is(err, store.ErrAttachmentNotFound) || (err == nil &&
		(attachment.ReleaseID == nil || *attachment.ReleaseID != release.ID)) {
		writeProblem(w, http.StatusNotFound, "Attachment not found", "")
		return
	}
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Cannot read the attachment", "")
		return
	}
	if h.attachments == nil {
		writeProblem(w, http.StatusServiceUnavailable, "Object storage not configured", "")
		return
	}

	object, info, err := h.attachments.Open(r.Context(), attachment.Object)
	if err != nil {
		writeProblem(w, http.StatusNotFound, "File no longer stored", "")
		return
	}
	defer object.Close()

	contentType, disposition := servedAs(attachment, r.URL.Query().Get("download") != "")
	header := w.Header()
	header.Set("Content-Security-Policy", attachmentPolicy)
	header.Set("X-Content-Type-Options", "nosniff")
	header.Set("Cross-Origin-Resource-Policy", "same-origin")
	header.Set("Cache-Control", "private, max-age=300")
	header.Set("Content-Type", contentType)
	header.Set("Content-Disposition", disposition)

	// Best effort: a server without write deadlines has nothing to extend.
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(attachmentWriteTimeout))
	http.ServeContent(w, r, attachment.Name, info.LastModified, object)
}

// servedAs decides how a file is sent: its own type and inline when the browser shows it without
// running anything of it, a download of opaque bytes otherwise.
func servedAs(attachment store.ResultAttachment, download bool) (contentType, disposition string) {
	base, _, err := mime.ParseMediaType(attachment.MediaType)
	inline := err == nil && inlineTypes[strings.ToLower(base)] && !download

	name := sanitizeFilename(attachment.Name)
	if inline {
		contentType = attachment.MediaType
		if strings.HasPrefix(base, "text/") && !strings.Contains(strings.ToLower(contentType), "charset") {
			contentType += "; charset=utf-8"
		}
		return contentType, mime.FormatMediaType("inline", map[string]string{"filename": name})
	}
	return "application/octet-stream", mime.FormatMediaType("attachment", map[string]string{"filename": name})
}
