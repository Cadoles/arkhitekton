package worker

import (
	"context"
	"errors"
	"testing"
	"time"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/event"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
)

/*
 * Les sondes.
 *
 * Trois choses s'éprouvent ici, et aucune ne s'éprouve à la lecture du code :
 * qu'une activation n'émet rien du passé, qu'une seconde passe sur la même
 * fenêtre de recouvrement ne renotifie pas, et qu'une forge injoignable
 * n'empêche pas les autres projets d'avancer.
 *
 * Elles valent la tranche à elles seules (doc 14 §14.11).
 */

// fakeProbe rend ce qu'on lui donne, et retient ce qu'on lui a demandé.
type fakeProbe struct {
	facts  []event.Event
	cursor string
	// err est rendue pour un projet nommé, pour éprouver l'isolation.
	errFor string
	// seen retient les curseurs reçus, projet par projet.
	seen map[string]string
}

func (p *fakeProbe) PluginID() string   { return "gitea" }
func (p *fakeProbe) Source() string     { return "gitea.pull_requests" }
func (p *fakeProbe) Schedule() Schedule { return Schedule{} }

func (p *fakeProbe) Poll(
	_ context.Context, project store.Project, _ map[string]string, cursor string,
) ([]event.Event, string, error) {
	if p.seen == nil {
		p.seen = map[string]string{}
	}
	p.seen[project.Slug] = cursor

	if p.errFor == project.Slug {
		return nil, "", errors.New("forge injoignable")
	}
	return p.facts, p.cursor, nil
}

// fakeProbeStore tient l'état que la boucle lit et écrit.
type fakeProbeStore struct {
	projects []store.Project
	cursors  map[string]string
	// facts retient les clés émises, comme la base : c'est l'unicité qu'on
	// éprouve, et un tableau ne dirait pas si un doublon a été écarté.
	facts map[string]event.Event
	// nouveaux compte les faits **neufs**, c'est-à-dire ceux que la boucle
	// aiguillerait. Le total ne suffit pas à le dire : une passe qui réécrit dix
	// fois le même fait laisse une carte d'un seul élément.
	nouveaux int
	failures []string
	disabled bool
}

func newFakeProbeStore(projects ...store.Project) *fakeProbeStore {
	return &fakeProbeStore{
		projects: projects,
		cursors:  map[string]string{},
		facts:    map[string]event.Event{},
	}
}

func (f *fakeProbeStore) ListProjects(context.Context) ([]store.Project, error) {
	return f.projects, nil
}

func (f *fakeProbeStore) ResolvedPlugin(
	_ context.Context, _ int64, pluginID string,
) (store.PluginSettings, error) {
	if f.disabled {
		return store.PluginSettings{}, store.ErrPluginDisabled
	}
	return store.PluginSettings{
		PluginID: pluginID, Enabled: true,
		Config: map[string]string{"url": "https://forge.example.fr", "token": "scellé:jeton"},
	}, nil
}

func (f *fakeProbeStore) GetSyncState(
	_ context.Context, source string, projectID int64,
) (store.SyncState, error) {
	return store.SyncState{Source: source, Cursor: f.cursors[curseurCle(source, projectID)]}, nil
}

func (f *fakeProbeStore) RecordSyncSuccess(
	_ context.Context, source string, projectID int64, cursor string,
) error {
	f.cursors[curseurCle(source, projectID)] = cursor
	return nil
}

func (f *fakeProbeStore) RecordSyncFailure(
	_ context.Context, source string, projectID int64, message string,
) error {
	f.failures = append(f.failures, source+":"+message)
	return nil
}

func (f *fakeProbeStore) RecordEvent(_ context.Context, fact event.Event) (bool, error) {
	if err := fact.Validate(); err != nil {
		return false, err
	}
	if _, known := f.facts[fact.DedupKey]; known {
		return false, nil
	}
	f.facts[fact.DedupKey] = fact
	f.nouveaux++
	return true, nil
}

func curseurCle(source string, projectID int64) string {
	return source + ":" + string(rune('0'+projectID))
}

func faitDeSonde(cle string) event.Event {
	return event.Event{
		Type: "gitea.pull_request.opened", Subject: "#412",
		Payload:    map[string]string{"number": "412", "title": "Corriger le clone"},
		OccurredAt: time.Date(2026, 9, 3, 10, 0, 0, 0, time.UTC),
		DedupKey:   cle,
	}
}

func projetDeTest(id int64, slug string) store.Project {
	return store.Project{ID: id, Slug: slug, Repository: "CNOUS/" + slug}
}

func runnerDeSonde(t *testing.T, store *fakeProbeStore, probe Probe) *ProbeRunner {
	t.Helper()
	return NewProbeRunner(store, openerDeTest{}, time.Minute, quietLogger(), probe)
}

/*
 * 1. Une activation ne réveille pas le passé.
 */

// La première passe **établit le curseur et ne déclenche rien**.
//
// Sans cette règle, activer une sonde sur un dépôt qui porte quatre cents
// demandes de fusion enverrait quatre cents notifications.
//
// Elle **retient** pourtant ce qu'elle voit, et c'est une correction apportée par
// la migration du suivi : voir le test suivant.
func TestUneActivationNeReveillePasLePasse(t *testing.T) {
	base := newFakeProbeStore(projetDeTest(1, "mse"))
	probe := &fakeProbe{
		facts:  []event.Event{faitDeSonde("gitea.pr:mse:410"), faitDeSonde("gitea.pr:mse:411")},
		cursor: "2026-09-03T10:00:00Z",
	}

	runnerDeSonde(t, base, probe).sweep(context.Background(), probe)

	if len(base.facts) != 2 {
		t.Errorf("%d faits retenus à l'activation, attendu 2", len(base.facts))
	}
	// Le curseur est posé : la passe suivante repartira de là.
	if base.cursors[curseurCle("gitea.pull_requests", 1)] != "2026-09-03T10:00:00Z" {
		t.Errorf("curseur = %q", base.cursors[curseurCle("gitea.pull_requests", 1)])
	}
}

// Une sonde qui rend un **instantané** ne déclenche pas après son amorçage.
//
// # Le défaut que la seconde réalisation a révélé
//
// L'amorçage jetait ses faits. Cela suffisait tant que toute sonde filtrait sur
// son curseur : la sonde Gitea ne rend que les demandes ouvertes depuis, et ne les
// revoit jamais.
//
// La synchronisation du suivi relit **toutes** les versions à chaque passe —
// l'API de Redmine ne les filtre pas. Jetés à l'amorçage sans être retenus, ces
// faits revenaient neufs au tour suivant et déclenchaient tout ce que l'amorçage
// prétendait taire. « Une activation ne réveille pas le passé » n'était vrai que
// d'un tour.
func TestUnInstantaneNeDeclenchePasApresSonAmorcage(t *testing.T) {
	base := newFakeProbeStore(projetDeTest(1, "mse"))
	// Une sonde d'instantané : les mêmes faits à chaque passe, indépendamment du
	// curseur. C'est exactement ce que fait la lecture des versions.
	probe := &fakeProbe{
		facts:  []event.Event{faitDeSonde("redmine.version:mse:803"), faitDeSonde("redmine.version:mse:805")},
		cursor: "2026-09-03T10:00:00Z",
	}
	runner := runnerDeSonde(t, base, probe)

	runner.sweep(context.Background(), probe) // amorçage
	apresAmorcage := base.nouveaux

	runner.sweep(context.Background(), probe) // le tour suivant revoit tout

	if base.nouveaux != apresAmorcage {
		t.Errorf("%d faits neufs après l'amorçage, attendu aucun — "+
			"l'instantané a redéclenché ce que l'amorçage devait taire",
			base.nouveaux-apresAmorcage)
	}
}

// La passe suivante, elle, émet.
func TestLaPasseSuivanteEmet(t *testing.T) {
	base := newFakeProbeStore(projetDeTest(1, "mse"))
	probe := &fakeProbe{cursor: "2026-09-03T10:00:00Z"}
	runner := runnerDeSonde(t, base, probe)

	// Amorçage.
	runner.sweep(context.Background(), probe)

	// Une demande de fusion s'ouvre.
	probe.facts = []event.Event{faitDeSonde("gitea.pr:mse:412")}
	probe.cursor = "2026-09-03T11:00:00Z"
	runner.sweep(context.Background(), probe)

	if len(base.facts) != 1 {
		t.Fatalf("%d faits émis, attendu 1", len(base.facts))
	}
	fait := base.facts["gitea.pr:mse:412"]
	// La boucle rattache le projet et la source ; la sonde n'a pas à s'en charger.
	if fait.ProjectID == nil || *fait.ProjectID != 1 {
		t.Errorf("projet du fait = %v", fait.ProjectID)
	}
	if fait.Source != "gitea" {
		t.Errorf("source du fait = %q, attendue l'identifiant du plugin", fait.Source)
	}
}

/*
 * 2. Une seconde passe sur la même fenêtre ne renotifie pas.
 */

// La sonde relit une fenêtre de recouvrement à chaque passe — c'est ce qui évite
// de perdre un fait à cheval sur deux secondes. Sans clé stable, chaque passe
// renotifierait.
func TestUneSecondePasseSurLaMemeFenetreNeRenotifiePas(t *testing.T) {
	base := newFakeProbeStore(projetDeTest(1, "mse"))
	probe := &fakeProbe{cursor: "2026-09-03T10:00:00Z"}
	runner := runnerDeSonde(t, base, probe)

	runner.sweep(context.Background(), probe) // amorçage

	probe.facts = []event.Event{faitDeSonde("gitea.pr:mse:412")}
	runner.sweep(context.Background(), probe)
	// La fenêtre de recouvrement fait revoir le même fait.
	runner.sweep(context.Background(), probe)
	runner.sweep(context.Background(), probe)

	if len(base.facts) != 1 {
		t.Errorf("%d faits enregistrés après trois passes, attendu 1", len(base.facts))
	}
}

/*
 * 3. Un échec est isolé par projet.
 */

// Une forge injoignable pour un projet n'empêche pas les autres d'avancer.
//
// Deux projets peuvent viser deux forges, et la panne de l'une ne dit rien de
// l'autre. C'est la règle que la synchronisation du suivi vient d'apprendre.
func TestUneForgeInjoignableNarretePasLesAutresProjets(t *testing.T) {
	base := newFakeProbeStore(
		projetDeTest(1, "casse"), projetDeTest(2, "sain"))
	probe := &fakeProbe{errFor: "casse", cursor: "2026-09-03T10:00:00Z"}
	runner := runnerDeSonde(t, base, probe)

	runner.sweep(context.Background(), probe) // amorçage des deux

	probe.facts = []event.Event{faitDeSonde("gitea.pr:sain:1")}
	runner.sweep(context.Background(), probe)

	// Le projet sain a bien émis.
	if len(base.facts) != 1 {
		t.Errorf("%d faits émis, attendu 1 pour le projet sain", len(base.facts))
	}
	// Et l'échec de l'autre est consigné, pour que l'écran le dise.
	if len(base.failures) == 0 {
		t.Error("l'échec du projet en panne n'a pas été consigné")
	}
	// Son curseur n'a **pas** avancé : la passe suivante reverra ce qu'il a raté.
	if base.cursors[curseurCle("gitea.pull_requests", 1)] != "" {
		t.Errorf("curseur du projet en panne = %q, attendu inchangé",
			base.cursors[curseurCle("gitea.pull_requests", 1)])
	}
}

/*
 * Ce que la boucle fait par ailleurs.
 */

// Un plugin éteint n'est pas interrogé, et ce n'est pas une panne : le signaler à
// chaque tour remplirait le journal d'un état voulu.
func TestUnPluginEteintNestPasInterroge(t *testing.T) {
	base := newFakeProbeStore(projetDeTest(1, "mse"))
	base.disabled = true
	probe := &fakeProbe{cursor: "x"}

	runnerDeSonde(t, base, probe).sweep(context.Background(), probe)

	if len(probe.seen) != 0 {
		t.Errorf("la sonde a été interrogée pour un plugin éteint: %v", probe.seen)
	}
	if len(base.failures) != 0 {
		t.Errorf("un plugin éteint a été consigné en échec: %v", base.failures)
	}
}

// Le champ scellé est ouvert avant d'atteindre la sonde ; les autres traversent
// tels quels.
func TestLesChampsScellesSontOuvertsAvantDatteindreLaSonde(t *testing.T) {
	base := newFakeProbeStore(projetDeTest(1, "mse"))

	var vu map[string]string
	probe := &lecteurDeConfig{cible: &vu}
	NewProbeRunner(base, openerDeTest{}, time.Minute, quietLogger(), probe).
		sweep(context.Background(), probe)

	if vu["token"] != "jeton" {
		t.Errorf("jeton transmis = %q, attendu descellé", vu["token"])
	}
	if vu["url"] != "https://forge.example.fr" {
		t.Errorf("URL transmise = %q", vu["url"])
	}
}

// lecteurDeConfig retient la configuration reçue.
type lecteurDeConfig struct{ cible *map[string]string }

func (l *lecteurDeConfig) PluginID() string   { return "gitea" }
func (l *lecteurDeConfig) Source() string     { return "gitea.pull_requests" }
func (l *lecteurDeConfig) Schedule() Schedule { return Schedule{} }
func (l *lecteurDeConfig) Poll(
	_ context.Context, _ store.Project, config map[string]string, _ string,
) ([]event.Event, string, error) {
	*l.cible = config
	return nil, "curseur", nil
}

// Le plancher d'intervalle est imposé : la charge se compte en *projets × sondes
// × fréquence*, et le multiplicande est une machine qui n'est pas la nôtre.
func TestLePlancherDIntervalleEstImpose(t *testing.T) {
	runner := NewProbeRunner(newFakeProbeStore(), nil, time.Second, quietLogger())

	if runner.interval < minProbeInterval {
		t.Errorf("intervalle = %s, plancher %s", runner.interval, minProbeInterval)
	}
}

// Une sonde déclare sa cadence, et le plancher du déploiement reste au-dessus.
//
// Le suivi se relit toutes les cinq minutes — une saisie humaine ne va pas plus
// vite —, une forge se regarde plus souvent. Une cadence unique obligerait les
// deux à s'accorder, et la plus lente ferait la loi.
func TestChaqueSondeDeclareSaCadenceAuDessusDuPlancher(t *testing.T) {
	// La sonde du suivi demande cinq minutes : au-dessus du plancher, respectées.
	if got := NewRedmineProbe(nil).Schedule(); got.Interval != syncInterval {
		t.Errorf("cadence du suivi = %s, attendu %s", got.Interval, syncInterval)
	}
	// Et elle réclame une passe immédiate : elle remplit un écran, qui serait vide
	// pendant cinq minutes après chaque démarrage.
	if !NewRedmineProbe(nil).Schedule().Eager {
		t.Error("la sonde du suivi doit réclamer une passe immédiate")
	}
	// La sonde de la forge ne constate que : aucune passe d'emblée, sans quoi un
	// redémarrage en boucle martèlerait une machine tierce.
	if NewGiteaPullProbe(0).Schedule().Eager {
		t.Error("une sonde qui ne fait que constater ne part pas d'emblée")
	}
}
