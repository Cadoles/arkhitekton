package worker

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/action"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/plan"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/webhook"
)

/*
 * The actions, seen from the engine.
 *
 * What is at stake here is not the HTTP call — `internal/webhook` takes care of that and
 * exercises it — but the **boundary of the secret**: a named token must reach the
 * authentication header without ever crossing a template, a body, or an error message.
 */

// fakeSecrets returns a token for a known designation.
type fakeSecrets struct {
	values map[string]string
	err    error
}

func (f fakeSecrets) ResolveSecret(_ context.Context, _ *int64, reference string) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	return f.values[reference], nil
}

func runnerFor(t *testing.T, secrets SecretResolver) *ActionRunner {
	t.Helper()
	return NewActionRunner(testActions, webhook.New(webhook.Options{
		AllowPrivate: true, AllowInsecure: true,
	}), secrets, quietLogger())
}

func projectRow(id int64) *int64 { return &id }

// The nominal path: the template is bound to the fact's payload, and the token leaves in the
// header — never in the body.
func TestAnOutgoingCallBindsItsTemplateAndSetsItsToken(t *testing.T) {
	var received struct {
		body string
		auth string
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		payload := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(payload)
		received.body = string(payload)
		received.auth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	runner := runnerFor(t, fakeSecrets{values: map[string]string{
		"redmine.api_key": "production-token",
	}})

	job, err := runner.Run(context.Background(), store.PlanRun{
		ID: 1, ProjectID: projectRow(7),
		Context: map[string]string{"tag": "1.8.0-rc1", "outcome": "succeeded"},
	}, plan.Geste{
		Kind: plan.GesteAction, Ref: action.Webhook,
		Params: map[string]string{
			"url":         server.URL,
			"body":        `{"text":"release {tag} : {outcome}"}`,
			"auth_header": "Authorization",
			"auth_secret": "redmine.api_key",
		},
	})
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	// An action is concluded the moment it is done: nothing will arrive later to conclude it, so
	// there is no job to wait for.
	if job != nil {
		t.Errorf("job identifier = %v, expected null", *job)
	}

	if received.body != `{"text":"release 1.8.0-rc1 : succeeded"}` {
		t.Errorf("body received = %q", received.body)
	}
	if received.auth != "production-token" {
		t.Errorf("authentication header = %q", received.auth)
	}
	// **The token is not in the body.** That is the rule: a sensitive field is named, never
	// interpolated.
	if strings.Contains(received.body, "production-token") {
		t.Error("the token shows through in the body")
	}
}

// An empty body sends the fact's payload as it is: a generic recipient receives everything
// known, with no template to write.
func TestAnEmptyBodySendsTheFactsPayload(t *testing.T) {
	var received string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		payload := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(payload)
		received = string(payload)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	_, err := runnerFor(t, nil).Run(context.Background(), store.PlanRun{
		Context: map[string]string{"tag": "1.8.0"},
	}, plan.Geste{
		Kind: plan.GesteAction, Ref: action.Webhook,
		Params: map[string]string{"url": server.URL},
	})
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if !strings.Contains(received, `"tag":"1.8.0"`) {
		t.Errorf("payload received = %q", received)
	}
}

// A template that yields invalid JSON is refused **before** leaving: the recipient would
// answer about *its* syntax, and the message would send someone hunting on their side for a
// defect that is on ours.
func TestATemplateYieldingInvalidJSONIsRefusedBeforeLeaving(t *testing.T) {
	var reached bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	_, err := runnerFor(t, nil).Run(context.Background(), store.PlanRun{
		Context: map[string]string{"tag": "1.8.0"},
	}, plan.Geste{
		Kind: plan.GesteAction, Ref: action.Webhook,
		Params: map[string]string{"url": server.URL, "body": `{"tag": {tag}}`},
	})

	if err == nil || !strings.Contains(err.Error(), "JSON") {
		t.Fatalf("%v, expected a refusal naming the JSON", err)
	}
	if reached {
		t.Error("the request left despite an invalid body")
	}
}

// The header and the token are declared together.
//
// A token with no header would go nowhere; a header with no token would go empty, and the
// recipient would refuse with an authentication message that would not say the value is
// missing on our side.
func TestTheHeaderAndTheTokenAreDeclaredTogether(t *testing.T) {
	runner := runnerFor(t, fakeSecrets{})

	for _, params := range []map[string]string{
		{"url": "https://example.test/h", "auth_header": "Authorization"},
		{"url": "https://example.test/h", "auth_secret": "redmine.api_key"},
	} {
		_, err := runner.Run(context.Background(), store.PlanRun{}, plan.Geste{
			Kind: plan.GesteAction, Ref: action.Webhook, Params: params,
		})
		if err == nil || !strings.Contains(err.Error(), "together") {
			t.Errorf("params %v: %v", params, err)
		}
	}
}

// A token that is named but empty is refused rather than sent empty: the recipient would
// answer 401, and the message would not say the setting is missing on our side.
func TestAnUnsetTokenIsRefused(t *testing.T) {
	_, err := runnerFor(t, fakeSecrets{values: map[string]string{}}).Run(
		context.Background(), store.PlanRun{ProjectID: projectRow(7)}, plan.Geste{
			Kind: plan.GesteAction, Ref: action.Webhook,
			Params: map[string]string{
				"url":         "https://example.test/h",
				"auth_header": "Authorization", "auth_secret": "redmine.api_key",
			},
		})

	if err == nil || !strings.Contains(err.Error(), "not set") {
		t.Fatalf("%v", err)
	}
}

// A forbidden target is refused by the transport, and the error stays recognisable through
// the runner: "refused" is fixed in the reaction, "unreachable" at the recipient.
func TestAForbiddenTargetStaysRecognisable(t *testing.T) {
	runner := NewActionRunner(testActions,
		webhook.New(webhook.Options{}), fakeSecrets{}, quietLogger())

	_, err := runner.Run(context.Background(), store.PlanRun{}, plan.Geste{
		Kind: plan.GesteAction, Ref: action.Webhook,
		Params: map[string]string{"url": "https://169.254.169.254/latest/"},
	})

	if !errors.Is(err, webhook.ErrBlockedTarget) {
		t.Fatalf("%v, expected ErrBlockedTarget", err)
	}
}

// A refusal from the recipient fails the step, with its code and an excerpt: "failed" alone
// teaches nothing and forces a re-run to see.
func TestARefusalFromTheRecipientFailsTheStepAndSaysSo(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte("token expired"))
	}))
	defer server.Close()

	_, err := runnerFor(t, nil).Run(context.Background(), store.PlanRun{}, plan.Geste{
		Kind: plan.GesteAction, Ref: action.Webhook,
		Params: map[string]string{"url": server.URL},
	})

	if err == nil {
		t.Fatal("a 401 did not fail the step")
	}
	for _, expected := range []string{"401", "token expired"} {
		if !strings.Contains(err.Error(), expected) {
			t.Errorf("message %q — it must carry %q", err, expected)
		}
	}
}

// A plugin action on a deployment that has not wired them is refused by name: reporting
// "sent" for a step nobody runs would be worse.
func TestAnUnwiredPluginActionIsRefusedByName(t *testing.T) {
	_, err := runnerFor(t, nil).Run(context.Background(), store.PlanRun{}, plan.Geste{
		Kind: plan.GesteAction, Ref: "gitea.status",
		Params: map[string]string{
			"commit": "abc1234", "state": "success", "context": "dashboard",
		},
	})

	if err == nil || !strings.Contains(err.Error(), "gitea") {
		t.Fatalf("%v — the refusal must name the expected plugin", err)
	}
}

// The split is read in the descriptor, never in a split of the reference.
//
// That is the trap this test holds: `forge.status` — an internal action of an earlier version
// — carries a dot and looks exactly like an action of the `forge` plugin. Handing it to the
// plugin runner would fail it on "no plugin declares", a message that sends one hunting in
// the wrong place.
func TestADotInAReferenceDoesNotMakeAPlugin(t *testing.T) {
	var entrusted bool
	runner := runnerFor(t, nil).WithPlugins(stepRunnerFunc(
		func(context.Context, store.PlanRun, plan.Geste) (*int64, error) {
			entrusted = true
			return nil, nil
		}))

	_, err := runner.Run(context.Background(), store.PlanRun{}, plan.Geste{
		Kind: plan.GesteAction, Ref: "forge.status",
	})

	if entrusted {
		t.Error("an unknown action was handed to the plugin runner")
	}
	if err == nil || !errors.Is(err, action.ErrUnknown) {
		t.Fatalf("%v, expected ErrUnknown", err)
	}
}

// stepRunnerFunc adapts a function into a [GesteRunner].
type stepRunnerFunc func(context.Context, store.PlanRun, plan.Geste) (*int64, error)

func (f stepRunnerFunc) Run(
	ctx context.Context, run store.PlanRun, geste plan.Geste,
) (*int64, error) {
	return f(ctx, run, geste)
}

// The routing separates the two kinds: the engine has no business knowing the difference
// between a pipeline, which becomes a job, and an action, which goes out.
func TestTheRoutingSeparatesTheTwoKindsOfStep(t *testing.T) {
	router := stepRouter{actions: runnerFor(t, nil)}

	_, err := router.Run(context.Background(), store.PlanRun{}, plan.Geste{
		Kind: "courriel", Ref: "x",
	})
	if err == nil || !strings.Contains(err.Error(), "unknown") {
		t.Errorf("unknown kind: %v", err)
	}

	// With no pipeline runner wired, a pipeline step fails rather than leaving into the void.
	if _, err := router.Run(context.Background(), store.PlanRun{}, plan.Geste{
		Kind: plan.GestePipeline, Ref: "unit-tests",
	}); err == nil {
		t.Error("a pipeline step left with no runner")
	}
}
