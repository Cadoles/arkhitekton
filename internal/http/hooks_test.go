package http

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/event"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/capability"
)

// pushReceiver turns a `push` delivery into one fact keyed on its body, and refuses a body that
// says so.
type pushReceiver struct{ called *int }

func (p pushReceiver) Receive(
	_ context.Context, target capability.Target, delivery capability.Delivery,
) ([]capability.Observation, error) {
	*p.called++
	if delivery.Event != "push" {
		return nil, nil
	}
	if string(delivery.Body) == "garbled" {
		return nil, errors.New("payload unreadable")
	}
	return []capability.Observation{{
		Suffix: "push", Subject: "main", Payload: map[string]string{"repository": target.Repository},
		OccurredAt: time.Unix(0, 0), Key: target.Repository + ":" + string(delivery.Body),
	}}, nil
}

type hooksFixture struct {
	projects map[string]store.Project
	disabled bool
	secret   string
	recorded map[string]event.Event
}

func (f *hooksFixture) ProjectBySlug(_ context.Context, slug string) (store.Project, error) {
	project, found := f.projects[slug]
	if !found {
		return store.Project{}, store.ErrProjectNotFound
	}
	return project, nil
}

func (f *hooksFixture) ResolvedPlugin(_ context.Context, _ int64, pluginID string) (store.PluginSettings, error) {
	if f.disabled {
		return store.PluginSettings{}, fmt.Errorf("%w: %s", store.ErrPluginDisabled, pluginID)
	}
	return store.PluginSettings{Config: map[string]string{"webhook_secret": f.secret}}, nil
}

func (f *hooksFixture) RecordReceivedEvent(_ context.Context, fact event.Event) (bool, error) {
	if _, known := f.recorded[fact.DedupKey]; known {
		return false, nil
	}
	f.recorded[fact.DedupKey] = fact
	return true, nil
}

func sign(secret, body string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(body))
	return hex.EncodeToString(mac.Sum(nil))
}

func receiving(t *testing.T) (*hooksFixture, *int, http.Handler) {
	t.Helper()
	called := 0
	registry := mustRegistry(
		capability.Plugin{
			ID: "forge", Label: "Forge", PerProject: true,
			Events:  []capability.EventSpec{{Suffix: "push", Label: "Poussée"}},
			Project: []capability.Field{{Name: "webhook_secret", Label: "Secret", Secret: true}},
			Webhook: &capability.Webhook{
				Secret: "webhook_secret", Signature: "X-Forge-Signature", Event: "X-Forge-Event",
				Scheme: capability.SignatureHMACSHA256, Receiver: pushReceiver{called: &called},
			},
		},
		capability.Plugin{ID: "quiet", Label: "Quiet"},
	)
	fixture := &hooksFixture{
		projects: map[string]store.Project{"shop": {ID: 7, Slug: "shop", Repository: "acme/shop"}},
		secret:   "s3cret", recorded: map[string]event.Event{},
	}
	handler := NewHooksHandler(registry, fixture,
		func(_ string, sealed map[string]string) (map[string]string, error) { return sealed, nil },
		time.Second, quietLogger())
	mux := http.NewServeMux()
	handler.Routes(mux, nil)
	return fixture, &called, mux
}

func deliver(handler http.Handler, path, event, signature, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	request.Header.Set("X-Forge-Event", event)
	if signature != "" {
		request.Header.Set("X-Forge-Signature", signature)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

// An authentic delivery records its facts against the project, to be routed by the worker; the same
// delivery again records nothing, which is what makes a replay harmless.
func TestAnAuthenticDeliveryRecordsItsFactsOnce(t *testing.T) {
	fixture, _, handler := receiving(t)

	for round, expectedNew := range []int{1, 0} {
		response := deliver(handler, "/hooks/forge/projects/shop", "push", sign("s3cret", "abc"), "abc")
		if response.Code != http.StatusAccepted {
			t.Fatalf("round %d: status %d, %s", round, response.Code, response.Body)
		}
		var got receivedVw
		if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if got.Facts != 1 || got.New != expectedNew {
			t.Errorf("round %d: %+v, expected 1 fact, %d new", round, got, expectedNew)
		}
	}

	fact, found := fixture.recorded["forge.push:acme/shop:abc"]
	if !found {
		t.Fatalf("recorded %v", fixture.recorded)
	}
	if fact.ProjectID == nil || *fact.ProjectID != 7 || fact.Type != "forge.push" || fact.Source != "forge" {
		t.Errorf("fact %+v", fact)
	}
}

// What the forge sends about something the plugin does not observe is acknowledged, not refused:
// a forge sends every event its hook is set for, and a refusal would show as a failure there.
func TestADeliveryAboutNothingObservedIsAcknowledged(t *testing.T) {
	fixture, _, handler := receiving(t)
	response := deliver(handler, "/hooks/forge/projects/shop", "issues", sign("s3cret", "{}"), "{}")
	if response.Code != http.StatusAccepted || len(fixture.recorded) != 0 {
		t.Errorf("status %d, recorded %v", response.Code, fixture.recorded)
	}
}

// Every refusal happens before the plugin reads the delivery, and records nothing.
func TestARefusedDeliveryIsNeverRead(t *testing.T) {
	cases := []struct {
		name     string
		path     string
		prepare  func(*hooksFixture)
		sig      string
		body     string
		expected int
	}{
		{"wrong signature", "/hooks/forge/projects/shop", nil, sign("other", "abc"), "abc", http.StatusUnauthorized},
		{"no signature", "/hooks/forge/projects/shop", nil, "", "abc", http.StatusUnauthorized},
		{"body altered", "/hooks/forge/projects/shop", nil, sign("s3cret", "abc"), "abd", http.StatusUnauthorized},
		{"no secret set", "/hooks/forge/projects/shop", func(f *hooksFixture) { f.secret = " " },
			sign(" ", "abc"), "abc", http.StatusForbidden},
		{"switched off", "/hooks/forge/projects/shop", func(f *hooksFixture) { f.disabled = true },
			sign("s3cret", "abc"), "abc", http.StatusNotFound},
		{"unknown project", "/hooks/forge/projects/nope", nil, sign("s3cret", "abc"), "abc", http.StatusNotFound},
		{"plugin receiving nothing", "/hooks/quiet/projects/shop", nil, sign("s3cret", "abc"), "abc", http.StatusNotFound},
		{"unknown plugin", "/hooks/nope/projects/shop", nil, sign("s3cret", "abc"), "abc", http.StatusNotFound},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fixture, called, handler := receiving(t)
			if c.prepare != nil {
				c.prepare(fixture)
			}
			response := deliver(handler, c.path, "push", c.sig, c.body)
			if response.Code != c.expected {
				t.Errorf("status %d, expected %d: %s", response.Code, c.expected, response.Body)
			}
			if *called != 0 || len(fixture.recorded) != 0 {
				t.Errorf("read %d times, recorded %v", *called, fixture.recorded)
			}
		})
	}
}

func TestAnOversizedDeliveryIsRefused(t *testing.T) {
	fixture, called, handler := receiving(t)
	body := strings.Repeat("a", maxDeliveryBytes+1)
	response := deliver(handler, "/hooks/forge/projects/shop", "push", sign("s3cret", body), body)
	if response.Code != http.StatusRequestEntityTooLarge || *called != 0 || len(fixture.recorded) != 0 {
		t.Errorf("status %d, read %d times", response.Code, *called)
	}
}

// What the plugin cannot read is said to the sender, who is authenticated by then.
func TestADeliveryThePluginCannotReadIsRefusedWithItsReason(t *testing.T) {
	_, _, handler := receiving(t)
	response := deliver(handler, "/hooks/forge/projects/shop", "push", sign("s3cret", "garbled"), "garbled")
	if response.Code != http.StatusUnprocessableEntity || !bytes.Contains(response.Body.Bytes(), []byte("payload unreadable")) {
		t.Errorf("status %d: %s", response.Code, response.Body)
	}
}

func TestTheSignatureSchemes(t *testing.T) {
	hmacHook := &capability.Webhook{Signature: "X-Sig", Scheme: capability.SignatureHMACSHA256, Prefix: "sha256="}
	tokenHook := &capability.Webhook{Signature: "X-Token", Scheme: capability.SignatureToken}
	header := func(name, value string) http.Header {
		h := http.Header{}
		h.Set(name, value)
		return h
	}
	body := []byte("payload")
	cases := []struct {
		name   string
		hook   *capability.Webhook
		header http.Header
		valid  bool
	}{
		{"prefixed hmac", hmacHook, header("X-Sig", "sha256="+sign("k", "payload")), true},
		{"upper-case hex", hmacHook, header("X-Sig", "sha256="+strings.ToUpper(sign("k", "payload"))), true},
		{"prefix missing", hmacHook, header("X-Sig", sign("k", "payload")), false},
		{"prefix alone", hmacHook, header("X-Sig", "sha256="), false},
		{"token", tokenHook, header("X-Token", "k"), true},
		{"wrong token", tokenHook, header("X-Token", "kk"), false},
		{"unknown scheme", &capability.Webhook{Signature: "X-Token", Scheme: "plain"}, header("X-Token", "k"), false},
	}
	for _, c := range cases {
		if got := authentic(c.hook, "k", c.header, body); got != c.valid {
			t.Errorf("%s: %v, expected %v", c.name, got, c.valid)
		}
	}
}
