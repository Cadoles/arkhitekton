package redmine

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The guarantee this package must hold: no write method reaches the network. Checked against a
// server that counts what it receives — if a request went through, the counter would say so.
func TestNoWriteReachesTheNetwork(t *testing.T) {
	reached := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached++
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := clientFor(t, server.URL)

	for _, method := range []string{
		http.MethodPost, http.MethodPut, http.MethodPatch,
		http.MethodDelete, "PROPFIND", "TRACE",
	} {
		request, err := http.NewRequestWithContext(context.Background(), method, server.URL+"/issues.json", nil)
		if err != nil {
			t.Fatalf("request %s: %v", method, err)
		}

		_, err = client.http.Do(request)
		if !errors.Is(err, ErrWriteForbidden) {
			t.Errorf("%s: error %v, expected ErrWriteForbidden", method, err)
		}
	}

	if reached != 0 {
		t.Errorf("%d write request(s) reached the server", reached)
	}
}

// Reading, on the other hand, must go through: a guard that blocks everything protects nothing,
// it breaks.
func TestReadingIsAllowed(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"user":{"id":7,"login":"lecteur","admin":false}}`))
	}))
	defer server.Close()

	account, err := clientFor(t, server.URL).CheckAccess(context.Background())
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if account.Login != "lecteur" || account.ID != 7 {
		t.Errorf("compte %+v", account)
	}
}

// The key travels in a header and never in the URL: a query string is logged by the server and
// copied into the Go client's error messages.
func TestTheKeyTravelsInTheHeaderAndNeverInTheURL(t *testing.T) {
	var seenHeader, seenQuery string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenHeader = r.Header.Get("X-Redmine-API-Key")
		seenQuery = r.URL.RawQuery
		w.Write([]byte(`{"user":{"id":1,"login":"x"}}`))
	}))
	defer server.Close()

	if _, err := clientFor(t, server.URL).CheckAccess(context.Background()); err != nil {
		t.Fatalf("read: %v", err)
	}

	if seenHeader != "test-key" {
		t.Errorf("authentication header %q", seenHeader)
	}
	if strings.Contains(seenQuery, "test-key") {
		t.Errorf("the key was passed in the query string: %q", seenQuery)
	}
}

// The key must appear in no error message: it is the path by which a secret most often ends up in
// a log.
func TestNoErrorCarriesTheKey(t *testing.T) {
	// A server closed at once, to provoke a transport error — the case where the Go client copies the
	// whole URL into its `url.Error`.
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := server.URL
	server.Close()

	client := clientFor(t, url)

	_, err := client.CheckAccess(context.Background())
	if err == nil {
		t.Fatal("a closed server should produce an error")
	}
	if strings.Contains(err.Error(), "test-key") {
		t.Errorf("the key leaks into the error: %v", err)
	}

	// The write refusal must not carry it either.
	request, _ := http.NewRequest(http.MethodPost, url+"/issues.json", nil)
	_, err = client.http.Do(request)
	if err != nil && strings.Contains(err.Error(), "test-key") {
		t.Errorf("the key leaks into the write refusal: %v", err)
	}
}

// With no URL and no key, the client refuses to exist rather than fail at the first call: the
// absence of configuration is a deployment decision.
func TestWithoutConfigurationNoClientIsBuilt(t *testing.T) {
	for _, options := range []Options{
		{},
		{URL: "https://redmine.example.org"},
		{APIKey: "key"},
		{URL: "   ", APIKey: "   "},
	} {
		if _, err := New(options); !errors.Is(err, ErrNotConfigured) {
			t.Errorf("%+v: error %v, expected ErrNotConfigured", options, err)
		}
	}
}

// The key travels with every request: in clear text over the network, it is readable by any
// intermediary, and this is access to production.
func TestACleartextURLIsRefusedOutsideLocalhost(t *testing.T) {
	if _, err := New(Options{URL: "http://redmine.example.org", APIKey: "key"}); err == nil {
		t.Error("a remote http URL should be refused")
	}

	// localhost stays allowed: the tests and a local tunnel expose nothing.
	if _, err := New(Options{URL: "http://127.0.0.1:3000", APIKey: "key"}); err != nil {
		t.Errorf("127.0.0.1 should be allowed: %v", err)
	}
}

// A key refused by Redmine is told apart from an outage: it is almost always a key that is
// revoked or without rights on the project, and the message must say so.
func TestARefusedKeyIsNamedAsSuch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()

	_, err := clientFor(t, server.URL).CheckAccess(context.Background())
	if err == nil {
		t.Fatal("a 401 should produce an error")
	}
	if !strings.Contains(err.Error(), "invalid key or without rights") {
		t.Errorf("message peu explicite: %v", err)
	}
}

func clientFor(t *testing.T, serverURL string) *Client {
	t.Helper()
	client, err := New(Options{URL: serverURL, APIKey: "test-key"})
	if err != nil {
		t.Fatalf("construction: %v", err)
	}
	return client
}
