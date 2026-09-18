package auth

import (
	"context"
	"errors"
	"fmt"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/config"
)

// Claims porte l'identité extraite du jeton d'identité.
type Claims struct {
	Subject  string `json:"sub"`
	Username string `json:"preferred_username"`
	Name     string `json:"name"`
	Email    string `json:"email"`
}

// Exchanger échange un code d'autorisation contre une identité vérifiée.
//
// L'interface existe pour que les handlers soient testables sans fournisseur
// d'identité : c'est le seul point du flux qui sort du processus.
type Exchanger interface {
	AuthCodeURL(state, nonce, codeChallenge string) string
	Exchange(ctx context.Context, code, codeVerifier, expectedNonce string) (Claims, error)
}

// ErrInvalidNonce signale un jeton dont le nonce ne correspond pas à la
// transaction en cours : jeton rejoué, ou obtenu pour une autre session.
var ErrInvalidNonce = errors.New("nonce du jeton d'identité invalide")

// Provider implémente Exchanger avec go-oidc.
type Provider struct {
	oauth    oauth2.Config
	verifier *oidc.IDTokenVerifier
}

// NewProvider interroge la découverte du fournisseur et prépare la vérification.
//
// La découverte a lieu au démarrage : un fournisseur injoignable doit empêcher
// le démarrage plutôt que de produire des erreurs d'authentification opaques au
// premier utilisateur.
//
// L'issuer doit correspondre au caractère près à la claim `iss`. Gitea annonce
// « https://forge.cadoles.com/ », slash final compris : l'omettre fait échouer
// la vérification avec un message qui n'y renvoie pas.
func NewProvider(ctx context.Context, cfg config.OIDCConfig) (*Provider, error) {
	provider, err := oidc.NewProvider(ctx, cfg.IssuerURL)
	if err != nil {
		return nil, fmt.Errorf("découverte OIDC de %q: %w", cfg.IssuerURL, err)
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
		return Claims{}, fmt.Errorf("échange du code d'autorisation: %w", err)
	}

	rawIDToken, ok := token.Extra("id_token").(string)
	if !ok || rawIDToken == "" {
		return Claims{}, errors.New("réponse sans jeton d'identité")
	}

	// Vérifie signature, issuer, audience et expiration.
	idToken, err := p.verifier.Verify(ctx, rawIDToken)
	if err != nil {
		return Claims{}, fmt.Errorf("vérification du jeton d'identité: %w", err)
	}

	// Le nonce lie le jeton à la transaction ouverte par /auth/login. Sans cette
	// vérification, un jeton valide obtenu ailleurs serait accepté ici.
	if idToken.Nonce != expectedNonce {
		return Claims{}, ErrInvalidNonce
	}

	var claims Claims
	if err := idToken.Claims(&claims); err != nil {
		return Claims{}, fmt.Errorf("lecture des claims: %w", err)
	}
	if claims.Subject == "" {
		claims.Subject = idToken.Subject
	}
	if claims.Subject == "" {
		return Claims{}, errors.New("jeton sans identifiant de sujet")
	}
	return claims, nil
}
