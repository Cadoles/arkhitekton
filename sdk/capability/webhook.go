package capability

import "context"

/*
 * What a source pushes, beside what a probe pulls.
 *
 * A delivery produces the same observations a probe would, under the same declared suffixes and
 * the same keys: a pull request received and a pull request polled are one fact, written once.
 * Nothing new is launched by a delivery itself; the reactions a project declares do that.
 *
 * The plugin declares how a delivery is authenticated and the dashboard checks it, before the
 * plugin reads a byte. A public entry point whose check each implementation had to remember is
 * one an implementation would forget.
 */

// SignatureScheme says how a delivery proves it comes from the source.
type SignatureScheme string

const (
	// SignatureHMACSHA256 is the hexadecimal HMAC-SHA256 of the body, keyed with the secret: Gitea,
	// GitHub, Bitbucket.
	SignatureHMACSHA256 SignatureScheme = "hmac-sha256"
	// SignatureToken is the secret itself, carried in a header: GitLab. Weaker, since a delivery
	// read in transit can be replayed with another body.
	SignatureToken SignatureScheme = "token"
)

// Webhook declares that a plugin receives deliveries, and how they are authenticated.
type Webhook struct {
	// Secret names the plugin setting holding the shared secret. It must be a declared field
	// marked Secret: the dashboard unseals it to check a delivery, and never returns it.
	Secret string
	// Signature names the header carrying the proof: `X-Gitea-Signature`.
	Signature string
	Scheme    SignatureScheme
	// Prefix is stripped from the header's value before comparing: `sha256=` for GitHub.
	Prefix string
	// Event names the header saying what a delivery is about: `X-Gitea-Event`.
	Event string
	// Receiver reads an authenticated delivery.
	Receiver Receiver
}

// Delivery is what a source pushed, once the dashboard has authenticated it.
type Delivery struct {
	// Event is the value of the header the [Webhook] names.
	Event string
	Body  []byte
}

// Receiver reads a delivery into observations.
//
// The delivery is already authenticated. A delivery about something the plugin does not observe
// returns no observation and no error: a forge sends every event its hook is set for.
type Receiver interface {
	Receive(ctx context.Context, target Target, delivery Delivery) ([]Observation, error)
}
