// Package web serves the interface, embedded in the binary.
//
// Embedded rather than mounted as a volume or deployed as a second container: the
// binary and the interface it serves are then the same artifact, versioned together.
// An interface deployed ahead of its API calls routes that do not exist yet, and the
// symptom — 404s on a page that displays — does not point at its cause.
package web

import (
	"crypto/sha256"
	"embed"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
)

// all: includes files starting with a dot, among them the .gitkeep that lets this
// directory exist in the repository without committing build output.
//
//go:embed all:dist
var assets embed.FS

// ErrNotBuilt reports an interface missing from the binary.
var ErrNotBuilt = errors.New("interface non construite")

// Handler serves the static files and returns index.html for any unknown route.
//
// That fallback is what makes deep URLs work: `/releases/1.8.0` is a browser route,
// not a file. Without it, reloading a page or following a shared link would give a
// 404 while internal navigation works — a classic failure, and a confusing one,
// because it never happens while clicking.
func Handler(logger *slog.Logger) (http.Handler, error) {
	dist, err := fs.Sub(assets, "dist")
	if err != nil {
		return nil, fmt.Errorf("root of the static files: %w", err)
	}

	index, err := fs.ReadFile(dist, "index.html")
	if err != nil {
		// A normal case in backend development: the front end was not built. Refusing to
		// start would be excessive — the API works.
		logger.Warn("interface not embedded: run `make web`", "error", err)
		return notBuiltHandler(), nil
	}

	policy := contentSecurityPolicy(index)
	logger.Info("embedded interface", "csp_script_src", policy.scriptSrc)

	files := http.FileServerFS(dist)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		policy.apply(w)

		clean := strings.TrimPrefix(r.URL.Path, "/")
		if clean == "" {
			serveIndex(w, index)
			return
		}

		if _, err := fs.Stat(dist, clean); err != nil {
			// A browser route, not a file: index.html, and certainly not a 404 — it is for the
			// client-side router to decide.
			serveIndex(w, index)
			return
		}

		// Files under `assets/` carry a fingerprint in their name: their content never
		// changes under a given name, so they can be cached indefinitely. index.html must
		// never be, or a deployment would stay invisible.
		if strings.HasPrefix(clean, "assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		}
		files.ServeHTTP(w, r)
	}), nil
}

func serveIndex(w http.ResponseWriter, index []byte) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(index)
}

func notBuiltHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = fmt.Fprintln(w, "Interface not built. Run `make web`, then rebuild the binary.")
	})
}

// inlineScript captures the content of index.html's inline scripts.
var inlineScript = regexp.MustCompile(`(?s)<script(?:\s[^>]*)?>(.*?)</script>`)

type csp struct {
	scriptSrc string
	value     string
}

func (p csp) apply(w http.ResponseWriter) {
	w.Header().Set("Content-Security-Policy", p.value)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "same-origin")
}

// contentSecurityPolicy builds a policy without `unsafe-inline`.
//
// index.html carries one inline script — the one applying the theme before the first
// render, without which a white flash appears for a frame on every load in dark
// theme. An inline script normally requires `unsafe-inline`, which empties the
// policy of its substance: it would then also allow the script an injection put
// there.
//
// The alternative is to allow that exact script by its hash. It is computed here
// from the file actually embedded: the day the script changes, the policy follows,
// with no intervention and no risk of forgetting.
func contentSecurityPolicy(index []byte) csp {
	sources := []string{"'self'"}

	for _, match := range inlineScript.FindAllSubmatch(index, -1) {
		// A <script src=…> has an empty body: it is covered by 'self'.
		if len(strings.TrimSpace(string(match[1]))) == 0 {
			continue
		}
		sum := sha256.Sum256(match[1])
		sources = append(sources, "'sha256-"+base64.StdEncoding.EncodeToString(sum[:])+"'")
	}

	scriptSrc := strings.Join(sources, " ")

	return csp{
		scriptSrc: scriptSrc,
		value: strings.Join([]string{
			"default-src 'self'",
			"script-src " + scriptSrc,
			// Tailwind produces inline styles for custom properties; forbidding them would break
			// the layout.
			"style-src 'self' 'unsafe-inline'",
			"img-src 'self' data:",
			// A run's videos and HTML reports, read from the API: same origin, and the reports are
			// sandboxed by the API's own policy on top of the frame's.
			"media-src 'self'",
			"frame-src 'self'",
			"font-src 'self'",
			// Fonts are self-hosted and the API is same-origin: no external destination is
			// legitimate.
			"connect-src 'self'",
			"frame-ancestors 'none'",
			"base-uri 'none'",
			"form-action 'self'",
		}, "; "),
	}
}
