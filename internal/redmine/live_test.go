package redmine

import (
	"context"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/provider"
	"os"
	"testing"
	"time"
)

/*
 * Vérification contre l'instance réelle.
 *
 * Gardé derrière `REDMINE_LIVE=1` et non derrière la seule présence de la clé :
 * celle-ci est renseignée dans l'environnement de développement courant, et faire
 * dépendre le test de sa présence ferait taper l'instance de **production** à
 * chaque `make test`. Une suite de tests qui sollicite un service tiers du client
 * finit par le solliciter au mauvais moment.
 *
 * Le reste des tests travaille sur des fixtures capturées ici, ce qui donne la
 * même couverture de décodage sans dépendance réseau.
 *
 *   REDMINE_LIVE=1 go test ./internal/redmine/ -run Live -v
 */
func liveClient(t *testing.T) (*Client, string) {
	t.Helper()
	if os.Getenv("REDMINE_LIVE") != "1" {
		t.Skip("REDMINE_LIVE non défini: vérification contre l'instance réelle ignorée")
	}

	client, err := New(Options{URL: os.Getenv("REDMINE_URL"), APIKey: os.Getenv("REDMINE_API_KEY")})
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	project := os.Getenv("REDMINE_PROJECT")
	if project == "" {
		t.Fatal("REDMINE_PROJECT est requis pour la vérification réelle")
	}
	return client, project
}

// Le décodage correspond-il au format réellement servi ? C'est la seule question
// que des fixtures ne peuvent pas trancher, puisqu'elles en sont issues.
func TestLiveLesVersionsSeDecodent(t *testing.T) {
	client, project := liveClient(t)

	versions, err := client.ListVersions(context.Background(), project)
	if err != nil {
		t.Fatalf("versions: %v", err)
	}
	if len(versions) == 0 {
		t.Fatal("aucune version rendue")
	}

	for _, version := range versions {
		if version.ID == 0 {
			t.Errorf("version sans identifiant: %+v", version)
		}
		if version.Name == "" {
			t.Errorf("version %d sans nom", version.ID)
		}
		switch version.Status {
		case "open", "locked", "closed":
		default:
			t.Errorf("version %s: statut inattendu %q", version.Name, version.Status)
		}
	}
	t.Logf("%d versions décodées", len(versions))
}

// La pagination doit ramener l'ensemble, et sans doublon. C'est la propriété qui
// casse en silence quand le tri n'est pas stable.
func TestLiveLesDemandesSePaginentSansDoublon(t *testing.T) {
	client, project := liveClient(t)

	issues, err := client.ListIssues(context.Background(), provider.IssueQuery{Project: project})
	if err != nil {
		t.Fatalf("demandes: %v", err)
	}
	if len(issues) == 0 {
		t.Fatal("aucune demande rendue")
	}

	seen := map[int64]int{}
	withVersion := 0
	for _, issue := range issues {
		seen[issue.ID]++
		if issue.VersionID != nil {
			withVersion++
		}
		if issue.Tracker == "" || issue.Status == "" {
			t.Errorf("demande %d incomplète: %+v", issue.ID, issue)
		}
	}
	for id, count := range seen {
		if count != 1 {
			t.Errorf("demande %d rendue %d fois", id, count)
		}
	}
	t.Logf("%d demandes, %d distinctes, %d rattachées à une version",
		len(issues), len(seen), withVersion)
}

// Le filtre incrémental doit rendre moins que tout : c'est ce qui fait l'intérêt de
// la synchronisation, et une valeur ignorée par Redmine passerait inaperçue.
func TestLiveLeFiltreIncrementalReduitLeVolume(t *testing.T) {
	client, project := liveClient(t)
	ctx := context.Background()

	tout, err := client.ListIssues(ctx, provider.IssueQuery{Project: project})
	if err != nil {
		t.Fatalf("tout: %v", err)
	}

	// Une borne dans le futur proche : rien ne peut avoir été modifié après.
	futur, err := client.ListIssues(ctx, provider.IssueQuery{
		Project: project,
		Since:   time.Now().Add(24 * time.Hour),
	})
	if err != nil {
		t.Fatalf("filtré: %v", err)
	}

	if len(futur) >= len(tout) {
		t.Errorf("le filtre `updated_on` semble ignoré: %d demandes contre %d sans filtre",
			len(futur), len(tout))
	}
	t.Logf("sans filtre %d, depuis demain %d", len(tout), len(futur))
}
