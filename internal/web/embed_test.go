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

// index.html embarque un script en ligne — celui qui applique le thème avant le
// premier rendu. L'autoriser par empreinte plutôt que par `unsafe-inline` est ce
// qui garde la politique utile : `unsafe-inline` autoriserait aussi le script
// qu'une injection y placerait.
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
		t.Errorf("empreinte du script en ligne absente\n  script-src = %s\n  attendu    = %s", policy.scriptSrc, expected)
	}

	// Le script externe est couvert par 'self' : lui calculer une empreinte de
	// corps vide autoriserait tout script vide de la page.
	empty := sha256.Sum256(nil)
	if strings.Contains(policy.scriptSrc, base64.StdEncoding.EncodeToString(empty[:])) {
		t.Error("une empreinte de corps vide a été émise pour le script externe")
	}
}

// Le calcul part du fichier réellement embarqué : modifier le script anti-flash
// doit déplacer l'empreinte sans intervention, sans quoi la politique bloquerait
// la page au premier changement.
func TestPolicyFollowsTheScriptContent(t *testing.T) {
	first := contentSecurityPolicy([]byte(`<script>var a = 1</script>`))
	second := contentSecurityPolicy([]byte(`<script>var a = 2</script>`))

	if first.scriptSrc == second.scriptSrc {
		t.Error("l'empreinte n'a pas suivi le changement de script")
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
	} {
		if !strings.Contains(policy.value, directive) {
			t.Errorf("directive absente: %s", directive)
		}
	}
}

// Une route du navigateur n'est pas un fichier : `/releases/1.8.0` doit rendre
// index.html. Sans ce repli, la navigation interne fonctionnerait et un
// rechargement ou un lien partagé donnerait un 404 — panne déroutante, parce
// qu'elle ne se produit jamais en cliquant.
func TestDeepLinkServesIndex(t *testing.T) {
	handler := newHandler(t)

	for _, path := range []string{"/", "/environments", "/releases/1.8.0/tests/unit", "/queue"} {
		response := get(t, handler, path)
		if response.Code != http.StatusOK {
			t.Errorf("%s = %d, attendu 200", path, response.Code)
			continue
		}
		if contentType := response.Header().Get("Content-Type"); !strings.HasPrefix(contentType, "text/html") {
			t.Errorf("%s: Content-Type = %q", path, contentType)
		}
	}
}

// index.html ne doit jamais être mis en cache : il porte les noms des fichiers
// empreintés, et un index caché rendrait un déploiement invisible.
func TestIndexIsNotCachedButAssetsAre(t *testing.T) {
	handler := newHandler(t)

	if got := get(t, handler, "/").Header().Get("Cache-Control"); got != "no-cache" {
		t.Errorf("Cache-Control de index.html = %q, attendu no-cache", got)
	}

	asset := firstAsset(t)
	if asset == "" {
		t.Skip("aucun asset embarqué: interface non construite")
	}
	response := get(t, handler, "/"+asset)
	if response.Code != http.StatusOK {
		t.Fatalf("%s = %d", asset, response.Code)
	}
	if got := response.Header().Get("Cache-Control"); !strings.Contains(got, "immutable") {
		t.Errorf("Cache-Control de %s = %q, attendu immutable", asset, got)
	}
}

func TestSecurityHeadersOnEveryResponse(t *testing.T) {
	response := get(t, newHandler(t), "/environments")

	for header, expected := range map[string]string{
		"X-Content-Type-Options": "nosniff",
		"Referrer-Policy":        "same-origin",
	} {
		if got := response.Header().Get(header); got != expected {
			t.Errorf("%s = %q, attendu %q", header, got, expected)
		}
	}
	if response.Header().Get("Content-Security-Policy") == "" {
		t.Error("Content-Security-Policy absent")
	}
}

func newHandler(t *testing.T) http.Handler {
	t.Helper()

	handler, err := Handler(discardLogger())
	if err != nil {
		t.Fatalf("construction: %v", err)
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
