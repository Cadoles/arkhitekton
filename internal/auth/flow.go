package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"sync"
	"time"
)

// FlowState holds what must survive the round trip to the identity provider,
// between /auth/login and /auth/callback.
//
// These values never reach the browser other than as the opaque `state`: the PKCE
// verifier and the nonce stay server-side. Handing them to the client — a cookie,
// a parameter — would offer them to whoever intercepts the redirect.
type FlowState struct {
	State        string
	Nonce        string
	CodeVerifier string
	// RedirectAfter is the page requested before authentication. Returning the user
	// to the home page after signing in again loses their context, often mid-task.
	RedirectAfter string
	CreatedAt     time.Time
}

// FlowStore keeps authentication transactions in memory.
//
// For tests and single-instance use only. The original reasoning — these states
// live a few seconds and losing one costs a sign-in — missed the case that
// matters: with several replicas and no session affinity, /auth/login is served
// by one and /auth/callback by another, and one sign-in in two fails.
//
// Production therefore uses [DBFlowStore]. This one stays useful where there is a
// single process, and spares the flow tests a database.
type FlowStore struct {
	mu     sync.Mutex
	states map[string]FlowState
	ttl    time.Duration
	now    func() time.Time
}

func NewFlowStore(ttl time.Duration) *FlowStore {
	if ttl <= 0 {
		ttl = 10 * time.Minute
	}
	return &FlowStore{
		states: make(map[string]FlowState),
		ttl:    ttl,
		now:    time.Now,
	}
}

// Begin creates an authentication transaction and returns its state.
//
// The context is ignored: it exists only so this store stays interchangeable with
// [DBFlowStore], which needs one.
func (s *FlowStore) Begin(_ context.Context, redirectAfter string) (FlowState, error) {
	state, err := randomToken(32)
	if err != nil {
		return FlowState{}, err
	}
	nonce, err := randomToken(32)
	if err != nil {
		return FlowState{}, err
	}
	verifier, err := randomToken(64)
	if err != nil {
		return FlowState{}, err
	}

	flow := FlowState{
		State:         state,
		Nonce:         nonce,
		CodeVerifier:  verifier,
		RedirectAfter: redirectAfter,
		CreatedAt:     s.now(),
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.evictExpiredLocked()
	s.states[state] = flow
	return flow, nil
}

var (
	// ErrUnknownState reports an absent `state`: a replay, an expiry, or an attempt to
	// forge a callback. All three are treated alike.
	ErrUnknownState = errors.New("unknown or expired authentication state")
)

// Consume returns the associated state and removes it.
//
// Single-use: a replayable `state` would allow replaying an intercepted
// authorization code. Removal happens whether the state is valid or expired.
func (s *FlowStore) Consume(_ context.Context, state string) (FlowState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	flow, found := s.states[state]
	if !found {
		return FlowState{}, ErrUnknownState
	}
	delete(s.states, state)

	if s.now().Sub(flow.CreatedAt) > s.ttl {
		return FlowState{}, ErrUnknownState
	}
	return flow, nil
}

// Len serves diagnostics and tests.
func (s *FlowStore) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.states)
}

// evictExpiredLocked purges abandoned transactions. Without it, every sign-in
// started and interrupted would leave an entry for good.
func (s *FlowStore) evictExpiredLocked() {
	cutoff := s.now().Add(-s.ttl)
	for key, flow := range s.states {
		if flow.CreatedAt.Before(cutoff) {
			delete(s.states, key)
		}
	}
}

// CodeChallenge derives the S256 PKCE challenge from the verifier. `plain` would
// protect nothing.
func CodeChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// randomToken produces an unpredictable value encoded as URL-safe base64.
func randomToken(bytes int) (string, error) {
	buf := make([]byte, bytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("random generation: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// NewCSRFToken produit un jeton de double soumission.
func NewCSRFToken() (string, error) {
	return randomToken(32)
}

// ConstantTimeEqual compares two tokens without a timing leak. A naive comparison
// lets a correct prefix be guessed from how long the answer takes.
func ConstantTimeEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
