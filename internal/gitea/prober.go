package gitea

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"forge.cadoles.com/vcarroy/mse-dashboard/internal/provider"
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

// Probe rend le constat d'accès du dépôt, ou l'erreur qui l'en empêche.
//
// Deux questions dans un seul appel, et c'est voulu : elles portent sur le même
// dépôt et se posent au même moment, celui où quelqu'un veut savoir si ce projet
// est prêt à construire. Les séparer en deux boutons obligerait à en actionner
// deux pour obtenir une réponse qui n'en fait qu'une.
func (p Prober) Probe(ctx context.Context, repository, publicKey string) (provider.Probe, error) {
	client, err := New(Options{
		URL: p.URL, Token: p.Token, Repository: repository, Timeout: p.Timeout,
	})
	if err != nil {
		return provider.Probe{}, fmt.Errorf("client: %w", err)
	}

	branch, err := client.DefaultBranch(ctx)
	if err != nil {
		return provider.Probe{}, err
	}
	if branch == "" {
		// Un dépôt sans branche par défaut existe mais n'a jamais reçu de
		// commit : le clone d'un pipeline y échouerait, et le dire ici évite de
		// l'apprendre en cours de build.
		return provider.Probe{}, errors.New("le dépôt est vide : aucune branche par défaut")
	}

	probe := provider.Probe{DefaultBranch: branch}
	probe.KeyPresent, probe.KeyNote = p.checkKey(ctx, client, publicKey)
	return probe, nil
}

// checkKey regarde si la clé du projet figure dans celles du dépôt.
//
// Rend **trois** états. L'absence de réponse n'est jamais traduite en « clé
// absente » : le jeton peut ne pas avoir le droit d'administration que Gitea
// exige pour lister les clés, et conclure à l'absence couperait alors les
// constructions d'un projet dont la clé est en place.
func (p Prober) checkKey(ctx context.Context, client *Client, publicKey string) (*bool, string) {
	if strings.TrimSpace(publicKey) == "" {
		return nil, "aucune clé propre n'a encore été posée pour ce projet"
	}

	keys, err := client.DeployKeys(ctx)
	if err != nil {
		// Le message d'origine est conservé : c'est lui qui distingue un jeton
		// sans droit d'une forge injoignable, et les deux se corrigent
		// différemment.
		return nil, "clés du dépôt illisibles (le jeton a-t-il le droit " +
			"d'administration ?) : " + err.Error()
	}

	for _, candidate := range keys {
		if SameKey(candidate, publicKey) {
			present := true
			return &present, "la clé du projet figure dans les clés de déploiement du dépôt"
		}
	}

	absent := false
	return &absent, fmt.Sprintf(
		"la clé du projet ne figure pas parmi les %d clé(s) de déploiement du dépôt",
		len(keys))
}
