package gitea

import (
	"context"
	"errors"
	"fmt"
	"time"
)

/*
 * Test d'accès à un dépôt qu'on vient de déclarer.
 *
 * Distinct du Client, et pas par commodité : un Client est lié à un dépôt à sa
 * construction, alors que le test porte sur un dépôt dont on ne sait pas encore
 * s'il est joignable. D'où un client par appel.
 *
 * Vivait dans `internal/http`, où il était le seul appel à une API extérieure du
 * paquet : la couche HTTP traduisait un échec en constat lisible **et** parlait à
 * Gitea. Le second rôle appartient ici, avec les autres lectures de la forge
 * (doc 12 §12.9, chantier 4).
 */

// Prober teste un dépôt par l'API Gitea.
type Prober struct {
	URL     string
	Token   string
	Timeout time.Duration
}

// Probe rend la branche par défaut du dépôt, ou l'erreur qui l'en empêche.
func (p Prober) Probe(ctx context.Context, repository string) (string, error) {
	client, err := New(Options{
		URL: p.URL, Token: p.Token, Repository: repository, Timeout: p.Timeout,
	})
	if err != nil {
		return "", fmt.Errorf("client: %w", err)
	}

	branch, err := client.DefaultBranch(ctx)
	if err != nil {
		return "", err
	}
	if branch == "" {
		// Un dépôt sans branche par défaut existe mais n'a jamais reçu de
		// commit : le clone d'un pipeline y échouerait, et le dire ici évite de
		// l'apprendre en cours de build.
		return "", errors.New("le dépôt est vide : aucune branche par défaut")
	}
	return branch, nil
}
