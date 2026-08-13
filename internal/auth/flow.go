package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"sync"
	"time"
)

// FlowState retient ce qui doit survivre à l'aller-retour vers le fournisseur
// d'identité, entre /auth/login et /auth/callback.
//
// Ces valeurs ne transitent pas par le navigateur autrement que sous forme du
// `state` opaque : le vérificateur PKCE et le nonce restent côté serveur. Les
// confier au client — cookie, paramètre — reviendrait à les offrir à qui
// intercepte la redirection.
type FlowState struct {
	State        string
	Nonce        string
	CodeVerifier string
	// RedirectAfter est la page demandée avant l'authentification. Ramener
	// l'utilisateur à l'accueil après une reconnexion lui fait perdre son
	// contexte, souvent au milieu d'une opération.
	RedirectAfter string
	CreatedAt     time.Time
}

// FlowStore conserve les transactions d'authentification en cours.
//
// En mémoire volontairement : ces états vivent quelques secondes, sont sans
// valeur après usage, et les perdre au redémarrage n'a pour effet que de
// demander à l'utilisateur de recommencer sa connexion. Les persister ferait
// payer une écriture en base à chaque tentative, pour un bénéfice nul.
//
// Conséquence assumée : avec plusieurs répliques d'API, la requête de retour
// doit atteindre la même instance. C'est le cas avec des sessions collantes ou
// une seule réplique ; au-delà, il faudra un stockage partagé.
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

// Begin crée une transaction d'authentification et rend son état.
func (s *FlowStore) Begin(redirectAfter string) (FlowState, error) {
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
	// ErrUnknownState signale un `state` absent : rejeu, expiration, ou tentative
	// de forger un retour d'authentification. Les trois se traitent pareil.
	ErrUnknownState = errors.New("state d'authentification inconnu ou expiré")
)

// Consume rend l'état associé et le retire.
//
// À usage unique : un `state` rejouable permettrait de rejouer un code
// d'autorisation intercepté. Le retrait a lieu que l'état soit valide ou expiré.
func (s *FlowStore) Consume(state string) (FlowState, error) {
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

// Len sert au diagnostic et aux tests.
func (s *FlowStore) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.states)
}

// evictExpiredLocked purge les transactions abandonnées. Sans elle, chaque
// connexion commencée puis interrompue laisserait une entrée à vie.
func (s *FlowStore) evictExpiredLocked() {
	cutoff := s.now().Add(-s.ttl)
	for key, flow := range s.states {
		if flow.CreatedAt.Before(cutoff) {
			delete(s.states, key)
		}
	}
}

// CodeChallenge dérive le challenge PKCE S256 du vérificateur. Gitea annonce
// `S256` parmi ses méthodes supportées ; `plain` ne protégerait de rien.
func CodeChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// randomToken produit une valeur imprévisible encodée en base64 URL.
func randomToken(bytes int) (string, error) {
	buf := make([]byte, bytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("génération aléatoire: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// NewCSRFToken produit un jeton de double soumission.
func NewCSRFToken() (string, error) {
	return randomToken(32)
}

// ConstantTimeEqual compare deux jetons sans fuite temporelle. Une comparaison
// naïve laisse deviner un préfixe correct par la durée de la réponse.
func ConstantTimeEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
