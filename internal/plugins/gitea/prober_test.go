package gitea

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// A probe says where the repository's deploy keys are listed, for the project's to be pasted
// there, and whether it is among them.
func TestAProbeSaysWhereToPasteTheKey(t *testing.T) {
	const key = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOpBnBR4N3gq9a1g0Pa1fwM7G2v0wUn6mJkx8W2bB4Fj"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/repos/team/app":
			_ = json.NewEncoder(w).Encode(map[string]string{"default_branch": "main"})
		case "/api/v1/repos/team/app/keys":
			if r.URL.Query().Get("page") != "1" {
				_ = json.NewEncoder(w).Encode([]any{})
				return
			}
			_ = json.NewEncoder(w).Encode([]map[string]any{{"key": key + " arkhitekton"}})
		default:
			t.Errorf("unexpected %s", r.URL.Path)
		}
	}))
	t.Cleanup(server.Close)

	probe, err := Prober{URL: server.URL + "/", Token: "t", Timeout: time.Second}.Probe(context.Background(), "team/app", key)
	if err != nil {
		t.Fatal(err)
	}
	if probe.KeysURL != server.URL+"/team/app/settings/keys" || probe.DefaultBranch != "main" ||
		probe.KeyPresent == nil || !*probe.KeyPresent {
		t.Errorf("probe = %+v", probe)
	}
}
