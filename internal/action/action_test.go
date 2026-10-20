package action

import (
	"errors"
	"maps"
	"strings"
	"testing"
)

// The registry can be read, and each action describes itself well enough for an editor to compose
// its form.
func TestEachActionDescribesItselfWellEnoughToBeEdited(t *testing.T) {
	ids := map[string]bool{}
	for _, descriptor := range Registry() {
		if ids[descriptor.ID] {
			t.Errorf("identifier %q declared twice", descriptor.ID)
		}
		ids[descriptor.ID] = true

		if descriptor.Label == "" || descriptor.Summary == "" {
			t.Errorf("action %q with no label or no summary", descriptor.ID)
		}
		for _, field := range descriptor.Fields {
			if field.Name == "" || field.Label == "" {
				t.Errorf("action %q: badly declared field %+v", descriptor.ID, field)
			}
		}
	}

	if !ids[Webhook] {
		t.Errorf("declared actions = %v", ids)
	}
}

// The registry opens in the other direction: a plugin declares its acts as it declares its facts,
// and the caller sees only one list.
//
// What this test holds is the junction: without it, any cause is wired to a frozen set of
// consequences.
func TestAPluginsActionsJoinTheRegistry(t *testing.T) {
	found := false
	for _, descriptor := range All() {
		if descriptor.Plugin == "" {
			continue
		}
		found = true
		// Prefixed by its plugin: two forges declaring `status` would otherwise produce indistinguishable
		// acts.
		if !strings.HasPrefix(descriptor.ID, descriptor.Plugin+".") {
			t.Errorf("action %q of plugin %q: unprefixed reference",
				descriptor.ID, descriptor.Plugin)
		}
		// No sensitive field, and that is structural: a plugin action speaks to its own boundary, already
		// set. It names no token, and therefore cannot serve to extract one towards a target typed
		// elsewhere.
		for _, field := range descriptor.Fields {
			if field.SecretRef {
				t.Errorf("action %q: the field %q names a secret",
					descriptor.ID, field.Name)
			}
		}
	}
	if !found {
		t.Fatal("no plugin declares an action — the junction is not wired")
	}

	// Resolved by the joined registry, without the caller having to know where it comes from.
	descriptor, err := Get("gitea.status")
	if err != nil {
		t.Fatalf("gitea.status: %v", err)
	}
	if descriptor.Plugin != "gitea" {
		t.Errorf("plugin = %q", descriptor.Plugin)
	}
}

// An unknown action names those that exist.
func TestAnUnknownActionNamesThoseThatExist(t *testing.T) {
	_, err := Get("slack.post")
	if !errors.Is(err, ErrUnknown) {
		t.Fatalf("%v, expected ErrUnknown", err)
	}
	if !strings.Contains(err.Error(), Webhook) {
		t.Errorf("message %q — it must name the internal actions", err)
	}
}

/*
 * The validation, done when a plan is recorded rather than when it runs.
 */

// A missing required parameter is refused, and named.
func TestAMissingRequiredParameterIsRefused(t *testing.T) {
	webhook, _ := Get(Webhook)

	err := webhook.Validate(map[string]string{"body": `{"tag":"{tag}"}`})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("%v, expected ErrInvalid", err)
	}
	if !strings.Contains(err.Error(), "URL") {
		t.Errorf("message %q — it must name the missing field", err)
	}
}

// A parameter the action does not declare is refused rather than ignored: ignored, it gives a
// screen that says "recorded" on a value nothing will read back.
func TestAnUnknownParameterIsRefused(t *testing.T) {
	webhook, _ := Get(Webhook)

	err := webhook.Validate(map[string]string{
		"url": "https://hooks.example.fr/x", "methode": "PUT",
	})
	if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "methode") {
		t.Errorf("%v", err)
	}
}

// The URL's shape is checked here; its **target** is checked when connecting, by the transport.
// The two do not replace each other: this one catches a typo, the second catches a forgery.
func TestTheURLShapeIsCheckedAtEntry(t *testing.T) {
	webhook, _ := Get(Webhook)

	for _, target := range []string{"hooks.example.fr", "/hook", "file:///etc/passwd"} {
		if err := webhook.Validate(map[string]string{"url": target}); !errors.Is(err, ErrInvalid) {
			t.Errorf("URL %q accepted: %v", target, err)
		}
	}

	if err := webhook.Validate(map[string]string{
		"url": "https://hooks.example.fr/services/abc",
	}); err != nil {
		t.Errorf("valid URL refused: %v", err)
	}
}

// A closed set is checked here rather than at the recipient's: an invented value would be refused
// by the forge, at the least useful moment.
//
// The list comes from the plugin's descriptor and not from a condition on the identifier: that is
// what will let a second forge have its own states without modifying this package.
func TestAValueOutsideTheClosedSetIsRefused(t *testing.T) {
	status, err := Get("gitea.status")
	if err != nil {
		t.Fatalf("gitea.status: %v", err)
	}

	base := map[string]string{"commit": "abc1234", "context": "dashboard"}
	invalid := maps.Clone(base)
	invalid["state"] = "vert"

	err = status.Validate(invalid)
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("%v", err)
	}
	if !strings.Contains(err.Error(), "success") {
		t.Errorf("message %q — it must name the allowed states", err)
	}

	for _, state := range []string{"pending", "success", "failure", "error"} {
		params := maps.Clone(base)
		params["state"] = state
		if err := status.Validate(params); err != nil {
			t.Errorf("state %q refused: %v", state, err)
		}
	}
}

// A closed set must not forbid the template that will fill it.
//
// Without that exception, a state could only be written literally, and a reaction that wants to
// mirror the triggering fact's outcome would become two reactions.
func TestATemplateCrossesAClosedSet(t *testing.T) {
	status, _ := Get("gitea.status")

	if err := status.Validate(map[string]string{
		"commit": "{git_commit}", "state": "{outcome}", "context": "dashboard",
	}); err != nil {
		t.Errorf("template refused on a closed-set field: %v", err)
	}
}

// A template with an unclosed brace would produce a literal value the recipient could not
// understand.
func TestAMismatchedTemplateIsRefused(t *testing.T) {
	webhook, _ := Get(Webhook)

	err := webhook.Validate(map[string]string{
		"url": "https://hooks.example.fr/x", "body": `{"tag":"{tag"}`,
	})
	if !errors.Is(err, ErrInvalid) {
		t.Errorf("%v", err)
	}
}

/*
 * L'interpolation.
 */

// The known variables are replaced with the fact's payload.
func TestKnownVariablesAreReplaced(t *testing.T) {
	got := Expand(`{"release":"{tag}","issue":"{outcome}"}`, map[string]string{
		"tag": "1.8.0-rc1", "outcome": "succeeded",
	})

	want := `{"release":"1.8.0-rc1","issue":"succeeded"}`
	if got != want {
		t.Errorf("template returned = %q", got)
	}
}

// **An unknown variable stays literal.**
//
// A `{tag}` visible in a chat message is noticed and fixed, whereas a silent hole reads as a
// release with no name — and nobody could say whether it is the template or the data that is
// missing.
func TestAnUnknownVariableStaysLiteral(t *testing.T) {
	got := Expand("release {tag} to {environment}", map[string]string{"tag": "1.8.0"})

	if got != "release 1.8.0 to {environment}" {
		t.Errorf("template returned = %q", got)
	}
}

// A brace opened with no closing one does not make the interpolation loop, and comes out as it is.
func TestAnOpenBraceDoesNotLoop(t *testing.T) {
	for _, template := range []string{"a {b", "{", "}", "{}", "{a}{", ""} {
		got := Expand(template, map[string]string{"a": "x"})
		if got == "" && template != "" {
			t.Errorf("template %q returned empty", template)
		}
	}

	// The nominal case stays right in the middle of the noise.
	if got := Expand("{a}{", map[string]string{"a": "x"}); got != "x{" {
		t.Errorf("template returned = %q", got)
	}
}

// A template with no variable passes through unchanged, with no needless allocation.
func TestATemplateWithNoVariablePassesThroughUnchanged(t *testing.T) {
	if got := Expand("plain text", map[string]string{"a": "x"}); got != "plain text" {
		t.Errorf("template returned = %q", got)
	}
}

// The main case, and the one that was found wrong: a JSON body.
//
// A naive sweep — first opening brace, first closing one — made `{"release":"{tag}"` a variable
// name, swallowed the real one and left the template literal. It is no contrived case: it is
// **the** shape of a webhook body.
func TestAJSONBodyIsNotSwallowedByItsBraces(t *testing.T) {
	got := Expand(`{"text":"release {tag} : {outcome}","nested":{"a":"{tag}"}}`,
		map[string]string{"tag": "1.8.0-rc1", "outcome": "succeeded"})

	want := `{"text":"release 1.8.0-rc1 : succeeded","nested":{"a":"1.8.0-rc1"}}`
	if got != want {
		t.Errorf("template returned = %q\nexpected          = %q", got, want)
	}
}

// What looks like a structure and not like a name passes through as it is.
func TestWhatIsNotANamePassesThroughUnchanged(t *testing.T) {
	testCases := map[string]string{
		`{}`:                `{}`,
		`{ }`:               `{ }`,
		`{"a":1}`:           `{"a":1}`,
		`{a b}`:             `{a b}`,
		`{tag with spaces}`: `{tag with spaces}`,
	}

	for template, want := range testCases {
		if got := Expand(template, map[string]string{"tag": "x"}); got != want {
			t.Errorf("template %q returned %q, expected %q", template, got, want)
		}
	}
}
