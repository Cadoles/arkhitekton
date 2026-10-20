package web

import (
	"crypto/sha256"
	"encoding/base64"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// index.html embeds an inline script — the one applying the theme before the first
// render. Allowing it by hash rather than by `unsafe-inline` is what keeps the policy
// useful: `unsafe-inline` would also allow the script an injection put there.
func TestPolicyAllowsInlineScriptByHashNotUnsafeInline(t *testing.T) {
	index := []byte(`<!doctype html><html><head>
<script>document.documentElement.classList.add('dark')</script>
<script type="module" src="/assets/app.js"></script>
</head><body></body></html>`)

	policy := contentSecurityPolicy(index)

	if strings.Contains(policy.value, "unsafe-inline") && strings.Contains(policy.scriptSrc, "unsafe-inline") {
		t.Fatalf("script-src autorise unsafe-inline: %s", policy.scriptSrc)
	}

	sum := sha256.Sum256([]byte("document.documentElement.classList.add('dark')"))
	expected := "'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"
	if !strings.Contains(policy.scriptSrc, expected) {
		t.Errorf("inline script hash missing\n  script-src = %s\n  expected   = %s", policy.scriptSrc, expected)
	}

	// The external script is covered by 'self': computing a hash of its empty body would
	// allow any empty script on the page.
	empty := sha256.Sum256(nil)
	if strings.Contains(policy.scriptSrc, base64.StdEncoding.EncodeToString(empty[:])) {
		t.Error("a hash of an empty body was emitted for the external script")
	}
}

// The computation starts from the file actually embedded: changing the anti-flash
// script must move the hash with no intervention, otherwise the policy would block
// the page at the first change.
func TestPolicyFollowsTheScriptContent(t *testing.T) {
	first := contentSecurityPolicy([]byte(`<script>var a = 1</script>`))
	second := contentSecurityPolicy([]byte(`<script>var a = 2</script>`))

	if first.scriptSrc == second.scriptSrc {
		t.Error("the hash did not follow the script change")
	}
}

func TestPolicyForbidsFramingAndExternalConnections(t *testing.T) {
	policy := contentSecurityPolicy([]byte("<html></html>"))

	for _, directive := range []string{
		"default-src 'self'",
		"frame-ancestors 'none'",
		"base-uri 'none'",
		"connect-src 'self'",
		"font-src 'self'",
		"media-src 'self'",
		"frame-src 'self'",
	} {
		if !strings.Contains(policy.value, directive) {
			t.Errorf("missing directive: %s", directive)
		}
	}
}

// A browser route is not a file: `/releases/1.8.0` must return index.html. Without
// that fallback, internal navigation would work while a reload or a shared link gave
// a 404 — a confusing failure, because it never happens while clicking.
func TestDeepLinkServesIndex(t *testing.T) {
	handler := newHandler(t)

	for _, path := range []string{"/", "/environments", "/releases/1.8.0/tests/unit", "/queue"} {
		response := get(t, handler, path)
		if response.Code != http.StatusOK {
			t.Errorf("%s = %d, expected 200", path, response.Code)
			continue
		}
		if contentType := response.Header().Get("Content-Type"); !strings.HasPrefix(contentType, "text/html") {
			t.Errorf("%s: Content-Type = %q", path, contentType)
		}
	}
}

// index.html must never be cached: it carries the names of the fingerprinted files,
// and a cached index would make a deployment invisible.
func TestIndexIsNotCachedButAssetsAre(t *testing.T) {
	handler := newHandler(t)

	if got := get(t, handler, "/").Header().Get("Cache-Control"); got != "no-cache" {
		t.Errorf("Cache-Control of index.html = %q, expected no-cache", got)
	}

	asset := firstAsset(t)
	if asset == "" {
		t.Skip("no embedded asset: the interface was not built")
	}
	response := get(t, handler, "/"+asset)
	if response.Code != http.StatusOK {
		t.Fatalf("%s = %d", asset, response.Code)
	}
	if got := response.Header().Get("Cache-Control"); !strings.Contains(got, "immutable") {
		t.Errorf("Cache-Control of %s = %q, expected immutable", asset, got)
	}
}

func TestSecurityHeadersOnEveryResponse(t *testing.T) {
	response := get(t, newHandler(t), "/environments")

	for header, expected := range map[string]string{
		"X-Content-Type-Options": "nosniff",
		"Referrer-Policy":        "same-origin",
	} {
		if got := response.Header().Get(header); got != expected {
			t.Errorf("%s = %q, expected %q", header, got, expected)
		}
	}
	if response.Header().Get("Content-Security-Policy") == "" {
		t.Error("Content-Security-Policy missing")
	}
}

func newHandler(t *testing.T) http.Handler {
	t.Helper()

	handler, err := Handler(discardLogger())
	if err != nil {
		t.Fatalf("building: %v", err)
	}
	if !isBuilt(t) {
		t.Skip("interface non construite: lancer `make web`")
	}
	return handler
}

func isBuilt(t *testing.T) bool {
	t.Helper()

	_, err := assets.ReadFile("dist/index.html")
	return err == nil
}

func firstAsset(t *testing.T) string {
	t.Helper()

	entries, err := assets.ReadDir("dist/assets")
	if err != nil {
		return ""
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			return "assets/" + entry.Name()
		}
	}
	return ""
}

func get(t *testing.T, handler http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()

	request := httptest.NewRequest(http.MethodGet, path, nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}
