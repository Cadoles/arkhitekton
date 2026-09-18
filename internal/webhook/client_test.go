package webhook

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

/*
 * The target check.
 *
 * It is the part of the repository where getting it wrong costs most: a URL typed into the
 * interface becomes a request issued from the cluster, by a process carrying a service account
 * token. The tests therefore bear first on what must be **refused**.
 */

func clientFor(options Options) *Client {
	if options.Timeout == 0 {
		options.Timeout = 2 * 1e9
	}
	return New(options)
}

// The forbidden targets, and the most dangerous first.
//
// `169.254.169.254` serves instance metadata at every hosting provider, credentials included. A
// forgery that reaches it does not steal an application's data: it steals the cluster.
func TestForbiddenTargetsAreRefused(t *testing.T) {
	client := clientFor(Options{})

	cas := map[string]string{
		"cloud metadata":      "https://169.254.169.254/latest/meta-data/",
		"metadata over IPv6":  "https://[::ffff:169.254.169.254]/latest/",
		"boucle locale":       "https://127.0.0.1/hook",
		"named loopback":      "https://localhost/hook",
		"boucle locale IPv6":  "https://[::1]/hook",
		"private range 10":    "https://10.0.0.5/hook",
		"private range 172":   "https://172.16.3.1/hook",
		"private range 192":   "https://192.168.1.10/hook",
		"carrier-grade NAT":   "https://100.64.1.1/hook",
		"unspecified address": "https://0.0.0.0/hook",
	}

	for nom, cible := range cas {
		_, err := client.Post(context.Background(), cible, nil, []byte(`{}`))
		if !errors.Is(err, ErrBlockedTarget) {
			t.Errorf("%s (%s): %v, expected ErrBlockedTarget", nom, cible, err)
		}
	}
}

// The reason accompanies the refusal: "refused" with no ground sends one hunting for an outage,
// whereas "link-local" is fixed in one read.
func TestTheRefusalNamesItsReason(t *testing.T) {
	_, err := clientFor(Options{}).Post(
		context.Background(), "https://169.254.169.254/", nil, []byte(`{}`))

	if err == nil {
		t.Fatal("metadata target accepted")
	}
	if !strings.Contains(err.Error(), "metadata") {
		t.Errorf("message %q — it must name what is refused", err)
	}
}

// Clear text is refused by default: the payload carries a release's tag and the token that
// authenticates it, and all of that would cross a readable network.
func TestClearTextIsRefusedByDefault(t *testing.T) {
	_, err := clientFor(Options{}).Post(
		context.Background(), "http://example.test/hook", nil, []byte(`{}`))

	if !errors.Is(err, ErrInsecureTarget) {
		t.Fatalf("%v, expected ErrInsecureTarget", err)
	}
	if !strings.Contains(err.Error(), "clair") {
		t.Errorf("message %q — it must say what would leak", err)
	}
}

// A scheme that is not HTTP is refused without even being dialled: `file://` and `gopher://` are
// classic forgery vectors.
func TestAForeignSchemeIsRefused(t *testing.T) {
	client := clientFor(Options{AllowInsecure: true, AllowPrivate: true})

	for _, cible := range []string{
		"file:///etc/passwd", "gopher://example.test/", "ftp://example.test/",
	} {
		if _, err := client.Post(context.Background(), cible, nil, nil); !errors.Is(err, ErrBlockedTarget) {
			t.Errorf("cible %q: %v", cible, err)
		}
	}
}

/*
 * Ce que l'autorisation explicite ouvre — et ce qu'elle n'ouvre pas.
 */

// A local deployment needs its loopback, and declares it.
func TestTheExplicitAuthorizationOpensPrivateTargets(t *testing.T) {
	serveur := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer jeton" {
			t.Errorf("authentication header missing: %v", r.Header.Get("Authorization"))
		}
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte("received"))
	}))
	defer serveur.Close()

	client := clientFor(Options{AllowPrivate: true, AllowInsecure: true})
	response, err := client.Post(context.Background(), serveur.URL,
		map[string]string{"Authorization": "Bearer jeton"}, []byte(`{"tag":"1.8.0"}`))
	if err != nil {
		t.Fatalf("appel: %v", err)
	}
	if !response.OK() || response.Status != http.StatusAccepted {
		t.Errorf("response = %+v", response)
	}
	if response.Body != "received" {
		t.Errorf("corps = %q", response.Body)
	}
}

// **It never reopens link-local.** Instance metadata is not a target a deployment wants to reach,
// and the blanket authorization must not make it reachable on the side.
func TestThePrivateAuthorizationDoesNotOpenLinkLocal(t *testing.T) {
	client := clientFor(Options{AllowPrivate: true, AllowInsecure: true})

	_, err := client.Post(context.Background(), "http://169.254.169.254/", nil, nil)
	if !errors.Is(err, ErrBlockedTarget) {
		t.Fatalf("%v, expected ErrBlockedTarget despite the private authorization", err)
	}
}

/*
 * The redirect, the obvious way around the check.
 */

// No redirect is followed: the recipient answers 302 towards the metadata, and an obliging client
// goes there.
func TestNoRedirectIsFollowed(t *testing.T) {
	var atteint bool
	serveur := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/suite" {
			atteint = true
			w.WriteHeader(http.StatusOK)
			return
		}
		http.Redirect(w, r, "/suite", http.StatusFound)
	}))
	defer serveur.Close()

	client := clientFor(Options{AllowPrivate: true, AllowInsecure: true})
	_, err := client.Post(context.Background(), serveur.URL+"/depart", nil, []byte(`{}`))

	if !errors.Is(err, ErrBlockedTarget) {
		t.Fatalf("%v, expected a redirect refusal", err)
	}
	if atteint {
		t.Error("the redirect was followed")
	}
}

/*
 * Ce que le client rapporte, et ce qu'il tait.
 */

// A refusal from the recipient is an **observation**, not a failure of the dashboard: the response
// is returned with its code, and the caller decides.
func TestARefusalFromTheRecipientIsAnObservation(t *testing.T) {
	serveur := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte("jeton invalide"))
	}))
	defer serveur.Close()

	client := clientFor(Options{AllowPrivate: true, AllowInsecure: true})
	response, err := client.Post(context.Background(), serveur.URL, nil, []byte(`{}`))
	if err != nil {
		t.Fatalf("a refusal from the recipient must not be an error: %v", err)
	}
	if response.OK() {
		t.Error("a 403 is announced as successful")
	}
	if response.Status != http.StatusForbidden || response.Body != "jeton invalide" {
		t.Errorf("response = %+v", response)
	}
}

// A 3xx is not a success: the redirect not having been followed, nobody received the payload.
func TestARedirectIsNotASuccess(t *testing.T) {
	if (Response{Status: http.StatusFound}).OK() {
		t.Error("a 302 is announced as successful")
	}
	for _, code := range []int{200, 201, 202, 204} {
		if !(Response{Status: code}).OK() {
			t.Errorf("%d is not announced as successful", code)
		}
	}
}

// The response is bounded: a hostile recipient would otherwise grow the worker's memory through an
// endless body.
func TestTheResponseIsBounded(t *testing.T) {
	serveur := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("a", 100_000)))
	}))
	defer serveur.Close()

	client := clientFor(Options{AllowPrivate: true, AllowInsecure: true, MaxResponseBytes: 64})
	response, err := client.Post(context.Background(), serveur.URL, nil, nil)
	if err != nil {
		t.Fatalf("appel: %v", err)
	}
	if len(response.Body) > 64 {
		t.Errorf("body of %d bytes, bounded at 64", len(response.Body))
	}
}

// The URL is redacted in the error messages: a chat webhook often carries its token in its query
// string, and an error message ends up in a log.
func TestTheURLIsRedactedInTheErrors(t *testing.T) {
	_, err := clientFor(Options{}).Post(context.Background(),
		"http://example.test/hook?token=secret-not-to-be-logged", nil, nil)

	if err == nil {
		t.Fatal("cleartext call accepted")
	}
	if strings.Contains(err.Error(), "secret-not-to-be-logged") {
		t.Errorf("the token shows through in %q", err)
	}
}

// A URL with no host is refused rather than dialled: `https:///hook` reaches nobody, and the
// network error would not say why.
func TestAURLWithNoHostIsRefused(t *testing.T) {
	for _, cible := range []string{"", "   ", "https:///hook", "not a url"} {
		if _, err := clientFor(Options{}).Post(
			context.Background(), cible, nil, nil); !errors.Is(err, ErrBlockedTarget) {
			t.Errorf("cible %q: %v", cible, err)
		}
	}
}
