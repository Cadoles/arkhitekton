package http

import (
	"context"
	"os"
	"testing"

	"forge.cadoles.com/vcarroy/mse-dashboard/internal/catalog"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/config"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/results"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/store"
)

/*
 * Le score d'une vraie release, calculé sur la vraie base.
 *
 * # Pourquoi ce test existe
 *
 * Le score par geste tient sur six tests à fixtures, dont un vérifié par
 * mutation. Aucun d'eux n'exécute la **requête de rattachement** — celle qui suit
 * la chaîne résultat → run → job → geste —, et c'est précisément la pièce que
 * seules de vraies données peuvent démentir.
 *
 * Ce chantier a déjà fourni quatre défauts que les tests ne voyaient pas : la
 * forme d'un secret de clé de déploiement, un `on conflict` sans prédicat, un
 * identifiant de release lu avant son insertion, et un ordre d'écriture qui
 * rendait des artefacts introuvables. Tous constatés sur le cluster, aucun en
 * test.
 *
 * # Comment le lancer
 *
 *	kubectl -n mse-dashboard port-forward svc/mse-dashboard-postgres 55432:5432 &
 *	SCORE_LIVE=1 \
 *	SCORE_DATABASE_URL='postgres://dashboard:…@localhost:55432/dashboard' \
 *	SCORE_RELEASE_TAG=3.2.0-rc15 \
 *	  go test -count=1 -run Live -v ./internal/http/
 *
 * **La base n'est pas jetable et n'est pas effacée** : ce test ne fait que lire.
 * C'est la différence avec les autres tests live du dépôt, qui montent leur
 * propre schéma.
 */
func TestLiveLeScoreDuneVraieRelease(t *testing.T) {
	if os.Getenv("SCORE_LIVE") != "1" {
		t.Skip("SCORE_LIVE non défini: vérification réelle ignorée")
	}
	url := os.Getenv("SCORE_DATABASE_URL")
	tag := os.Getenv("SCORE_RELEASE_TAG")
	if url == "" || tag == "" {
		t.Fatal("SCORE_DATABASE_URL et SCORE_RELEASE_TAG sont requis")
	}

	ctx := context.Background()
	db, err := store.Open(ctx, config.DatabaseConfig{DSN: url, MaxConns: 2})
	if err != nil {
		t.Fatalf("ouverture de la base: %v", err)
	}
	defer db.Close()

	registry, err := catalog.Load("../../deploy/base/config/catalog.yaml")
	if err != nil {
		t.Fatalf("catalogue: %v", err)
	}

	// Le projet est celui que la release désigne : ces tests ne choisissent pas,
	// ils constatent.
	release, err := releaseParTag(ctx, db, tag)
	if err != nil {
		t.Fatalf("release %q: %v", tag, err)
	}

	reports, err := db.ListReleaseTestResults(ctx, release.ID)
	if err != nil {
		t.Fatalf("résultats: %v", err)
	}

	breakdown, err := computeScore(ctx, db, release.ID, reports, registry)
	if err != nil {
		t.Fatalf("calcul du score: %v", err)
	}

	t.Logf("release %s: score %d (mesuré: %v)", tag, breakdown.Score, breakdown.Measured)
	for _, composant := range breakdown.Signals {
		t.Logf("  %-14s %-22s poids déclaré %5.1f  effectif %5.1f  valeur %5.1f  %s",
			composant.Key, composant.Label,
			composant.Declared, composant.Weight, composant.Value, composant.Detail)
	}

	/*
	 * Ce que ce test **tient**, plutôt que ce qu'il observe.
	 */

	// Les composants viennent des gestes, pas des familles. C'est le changement
	// d'axe : une release qui a un plan ne doit plus rendre de composant `signal:`.
	for _, composant := range breakdown.Signals {
		if composant.Key == results.SignalKey(composant.Signal) {
			t.Errorf("composant %q identifié par son signal — le repli par famille "+
				"s'est appliqué à une release qui a pourtant un plan", composant.Key)
		}
		// Un composant sans libellé ne s'affiche pas : la case serait muette.
		if composant.Label == "" {
			t.Errorf("composant %q sans libellé", composant.Key)
		}
	}

	// Les poids effectifs des composants mesurés somment à 100. C'est la
	// renormalisation : un 100 sur deux gestes ne doit pas ressembler à un 100 sur
	// six, et c'est ce partage qui le dit.
	var effectif float64
	for _, composant := range breakdown.Signals {
		if composant.Measured {
			effectif += composant.Weight
		}
	}
	if breakdown.Measured && (effectif < 99.5 || effectif > 100.5) {
		t.Errorf("somme des poids effectifs = %.2f, attendue 100", effectif)
	}

	// Et chaque composant retiré du calcul est nommé : le lecteur doit savoir sur
	// quoi porte le score qu'il lit.
	retires := 0
	for _, composant := range breakdown.Signals {
		if !composant.Measured {
			retires++
		}
	}
	if retires != len(breakdown.Redistributed) {
		t.Errorf("%d composant(s) non mesuré(s) et %d annoncé(s) redistribué(s)",
			retires, len(breakdown.Redistributed))
	}
}

// releaseParTag retrouve une release par son tag, quel que soit son projet.
func releaseParTag(ctx context.Context, db *store.DB, tag string) (store.Release, error) {
	projects, err := db.ListProjects(ctx)
	if err != nil {
		return store.Release{}, err
	}
	for _, project := range projects {
		release, err := db.GetRelease(ctx, project.ID, tag)
		if err == nil {
			return release, nil
		}
	}
	return store.Release{}, store.ErrReleaseNotFound
}
