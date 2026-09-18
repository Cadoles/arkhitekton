package gitea

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

func clientFor(t *testing.T, serverURL string) *Client {
	t.Helper()
	client, err := New(Options{URL: serverURL, Token: "test-token", Repository: "CNOUS/mse"})
	if err != nil {
		t.Fatalf("construction: %v", err)
	}
	return client
}

// The package's guarantee: no write method reaches the network. The day the dashboard
// sets tags on the forge, it will be another client.
func TestNoWriteReachesTheNetwork(t *testing.T) {
	reached := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached++
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := clientFor(t, server.URL)
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		request, _ := http.NewRequestWithContext(context.Background(), method,
			server.URL+"/api/v1/repos/CNOUS/mse/tags", nil)
		if _, err := client.http.Do(request); !errors.Is(err, ErrWriteForbidden) {
			t.Errorf("%s: error %v, expected ErrWriteForbidden", method, err)
		}
	}
	if reached != 0 {
		t.Errorf("%d write request(s) reached the server", reached)
	}
}

// The token travels in a header, never in the URL, and leaks into no error.
func TestTheTokenStaysInTheHeaderAndOutOfErrors(t *testing.T) {
	var header, query string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header = r.Header.Get("Authorization")
		query = r.URL.RawQuery
		w.Write([]byte(`{"default_branch":"develop"}`))
	}))
	defer server.Close()

	if _, err := clientFor(t, server.URL).DefaultBranch(context.Background()); err != nil {
		t.Fatalf("lecture: %v", err)
	}
	if header != "token test-token" {
		t.Errorf("header %q", header)
	}
	if strings.Contains(query, "test-token") {
		t.Errorf("the token was passed in the URL: %q", query)
	}

	// A transport error: the full URL is copied into url.Error, and the token must not be
	// in it.
	dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	deadURL := dead.URL
	dead.Close()
	_, err := clientFor(t, deadURL).DefaultBranch(context.Background())
	if err == nil || strings.Contains(err.Error(), "test-token") {
		t.Errorf("the token leaks into the error: %v", err)
	}
}

// Pagination gathers the pages and stops on the short page.
func TestBranchesPageUntilTheShortPage(t *testing.T) {
	pages := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pages++
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		count := pageSize
		if page == 2 {
			count = 3 // page courte : la liste est finie
		}
		var payload []map[string]any
		for i := 0; i < count; i++ {
			payload = append(payload, map[string]any{
				"name":   fmt.Sprintf("branche-%d-%d", page, i),
				"commit": map[string]any{"id": "abc"},
			})
		}
		json.NewEncoder(w).Encode(payload)
	}))
	defer server.Close()

	refs, truncated, err := clientFor(t, server.URL).ListBranches(context.Background())
	if err != nil {
		t.Fatalf("branches: %v", err)
	}
	if len(refs) != pageSize+3 || pages != 2 {
		t.Errorf("%d branches over %d pages, expected %d over 2", len(refs), pages, pageSize+3)
	}
	if truncated {
		t.Error("a complete list marked truncated")
	}
}

// The bound cuts an endless list, and says so: a silently incomplete list would read
// as exhaustive.
func TestAnEndlessListIsBoundedAndSaysSo(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		var payload []map[string]any
		for i := 0; i < pageSize; i++ {
			payload = append(payload, map[string]any{
				"name": fmt.Sprintf("tag-%d", i), "commit": map[string]any{"sha": "abc"},
			})
		}
		json.NewEncoder(w).Encode(payload)
	}))
	defer server.Close()

	refs, truncated, err := clientFor(t, server.URL).ListTags(context.Background())
	if err != nil {
		t.Fatalf("tags: %v", err)
	}
	if len(refs) != maxRefs {
		t.Errorf("%d references, expected the bound %d", len(refs), maxRefs)
	}
	if !truncated {
		t.Error("truncation must be reported")
	}
}

// Without complete configuration, no client: absence is a deployment decision, not a
// fault to discover at the first call.
func TestWithoutConfigurationNoClientIsBuilt(t *testing.T) {
	for _, options := range []Options{
		{},
		{URL: "https://forge.example.org", Token: "x"},
		{URL: "https://forge.example.org", Repository: "CNOUS/mse"},
		{Token: "x", Repository: "CNOUS/mse"},
	} {
		if _, err := New(options); !errors.Is(err, ErrNotConfigured) {
			t.Errorf("%+v: error %v, expected ErrNotConfigured", options, err)
		}
	}

	// A repository without an owner is a malformed value, distinct from an absent one.
	if _, err := New(Options{URL: "https://forge.example.org", Token: "x", Repository: "mse"}); err == nil {
		t.Error("a repository without an owner should be refused")
	}
}

// The token opens the whole forge in its holder's name: in clear text over the
// network, out of the question.
func TestACleartextURLIsRefusedOutsideLocalhost(t *testing.T) {
	if _, err := New(Options{URL: "http://forge.example.org", Token: "x", Repository: "a/b"}); err == nil {
		t.Error("a remote http URL should be refused")
	}
	if _, err := New(Options{URL: "http://127.0.0.1:3000", Token: "x", Repository: "a/b"}); err != nil {
		t.Errorf("127.0.0.1 should be allowed: %v", err)
	}
}

// A refused token is told apart from a fault, and the message points somewhere.
func TestARefusedTokenIsNamedAsSuch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()

	_, err := clientFor(t, server.URL).DefaultBranch(context.Background())
	if err == nil || !strings.Contains(err.Error(), "invalid token") {
		t.Errorf("unhelpful message: %v", err)
	}
}
