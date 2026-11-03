package rocketchat

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"forge.cadoles.com/vcarroy/arkhitekton/sdk/capability"
)

func TestAMessageGoesToTheWebhook(t *testing.T) {
	var received map[string]string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&received)
	}))
	defer server.Close()

	target := capability.Target{Settings: capability.Settings{webhookURL: server.URL + "/hooks/a/b"}, Timeout: time.Second}
	err := executor{client: server.Client()}.Execute(context.Background(), target, "message",
		map[string]string{"text": "1.0.0 déployée sur unstable\n- #1", "channel": "#livraisons"})
	if err != nil {
		t.Fatal(err)
	}
	if received["text"] != "1.0.0 déployée sur unstable\n- #1" || received["channel"] != "#livraisons" {
		t.Errorf("received %v", received)
	}
}

func TestAFailureDoesNotQuoteTheWebhooksToken(t *testing.T) {
	target := capability.Target{Settings: capability.Settings{webhookURL: "http://127.0.0.1:1/hooks/id/secret-token"}, Timeout: time.Second}
	err := executor{client: &http.Client{}}.Execute(context.Background(), target, "message", map[string]string{"text": "x"})
	if err == nil || strings.Contains(err.Error(), "secret-token") {
		t.Errorf("err = %v", err)
	}
}
