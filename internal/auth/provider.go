package auth

import (
	"context"
	"errors"
	"fmt"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/config"
)

// Claims carries the identity extracted from the ID token.
type Claims struct {
	Subject  string `json:"sub"`
	Username string `json:"preferred_username"`
	Name     string `json:"name"`
	Email    string `json:"email"`
}

// Exchanger exchanges an authorization code for a verified identity.
//
// The interface exists so the handlers are testable without an identity provider:
// it is the only point of the flow that leaves the process.
type Exchanger interface {
	AuthCodeURL(state, nonce, codeChallenge string) string
	Exchange(ctx context.Context, code, codeVerifier, expectedNonce string) (Claims, error)
}

// ErrInvalidNonce reports a token whose nonce does not match the transaction in
// flight: a replayed token, or one obtained for another session.
var ErrInvalidNonce = errors.New("invalid ID token nonce")

// Provider implements Exchanger with go-oidc.
type Provider struct {
	oauth    oauth2.Config
	verifier *oidc.IDTokenVerifier
}

// NewProvider queries the provider's discovery document and prepares verification.
//
// Discovery happens at start-up: an unreachable provider must prevent start-up
// rather than produce opaque authentication errors for the first user.
//
// The issuer must match the `iss` claim character for character, trailing slash
// included; omitting it fails verification with a message that does not point at
// it.
func NewProvider(ctx context.Context, cfg config.OIDCConfig) (*Provider, error) {
	provider, err := oidc.NewProvider(ctx, cfg.IssuerURL)
	if err != nil {
		return nil, fmt.Errorf("OIDC discovery of %q: %w", cfg.IssuerURL, err)
	}

	return &Provider{
		oauth: oauth2.Config{
			ClientID:     cfg.ClientID,
			ClientSecret: cfg.ClientSecret,
			RedirectURL:  cfg.RedirectURL,
			Endpoint:     provider.Endpoint(),
			Scopes:       cfg.Scopes,
		},
		verifier: provider.Verifier(&oidc.Config{ClientID: cfg.ClientID}),
	}, nil
}

func (p *Provider) AuthCodeURL(state, nonce, codeChallenge string) string {
	return p.oauth.AuthCodeURL(state,
		oidc.Nonce(nonce),
		oauth2.SetAuthURLParam("code_challenge", codeChallenge),
		oauth2.SetAuthURLParam("code_challenge_method", "S256"),
	)
}

func (p *Provider) Exchange(ctx context.Context, code, codeVerifier, expectedNonce string) (Claims, error) {
	token, err := p.oauth.Exchange(ctx, code,
		oauth2.SetAuthURLParam("code_verifier", codeVerifier))
	if err != nil {
		return Claims{}, fmt.Errorf("exchanging the authorization code: %w", err)
	}

	rawIDToken, ok := token.Extra("id_token").(string)
	if !ok || rawIDToken == "" {
		return Claims{}, errors.New("response without an ID token")
	}

	// Vérifie signature, issuer, audience et expiration.
	idToken, err := p.verifier.Verify(ctx, rawIDToken)
	if err != nil {
		return Claims{}, fmt.Errorf("verifying the ID token: %w", err)
	}

	// The nonce binds the token to the transaction opened by /auth/login. Without this
	// check, a valid token obtained elsewhere would be accepted here.
	if idToken.Nonce != expectedNonce {
		return Claims{}, ErrInvalidNonce
	}

	var claims Claims
	if err := idToken.Claims(&claims); err != nil {
		return Claims{}, fmt.Errorf("reading the claims: %w", err)
	}
	if claims.Subject == "" {
		claims.Subject = idToken.Subject
	}
	if claims.Subject == "" {
		return Claims{}, errors.New("token without a subject identifier")
	}
	return claims, nil
}
