package worker

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"forge.cadoles.com/vcarroy/mse-dashboard/internal/event"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/provider"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/store"
)

/*
 * La synchronisation multi-projet.
 *
 * Le défaut qu'elle corrige était documenté et assumé : un curseur unique avançait
 * sur le premier projet lu, et le second reprenait depuis un instant où **il**
 * n'avait rien lu. Son historique était sauté, définitivement, sans message.
 *
 * # Ces tests pilotent maintenant la boucle des sondes
 *
 * Et c'est le cœur de la migration. Les propriétés qu'ils tiennent — un curseur
 * par projet, une connexion par projet, un échec isolé, une clé descellée avant
 * l'appel — n'appartiennent plus au suivi : elles appartiennent à `ProbeRunner`,
 * qui les tient pour **toutes** les sources.
 *
 * Les assertions n'ont pas changé. Ce qui a changé, c'est qui les honore — et
 * c'est la seule preuve qui vaille que le contrat a absorbé cette source.
 */

// multiStore rend deux projets et retient ce que chacun a écrit.
//
// Il réalise les deux contrats à la fois : celui de la boucle — projets,
// réglages, curseurs, faits — et celui de la projection — versions, demandes,
// adoptions, correspondance des statuts.
type multiStore struct {
	fakeRedmineStore
	// configs porte la connexion résolue de chaque projet, clé **scellée** comme
	// en base.
	configs map[int64]map[string]string
	// cursors retient le curseur enregistré par projet, et c'est le cœur du sujet.
	cursors  map[int64]string
	failures map[int64]string
	mappings map[int64]map[string]string
	faits    map[string]event.Event
}

func newMultiStore() *multiStore {
	return &multiStore{
		configs: map[int64]map[string]string{
			1: {"url": "https://redmine.un.fr", "api_key": "scellé:clé-un", "project": "projet-un"},
			2: {"url": "https://redmine.deux.fr", "api_key": "scellé:clé-deux", "project": "projet-deux"},
		},
		cursors:  map[int64]string{},
		failures: map[int64]string{},
		mappings: map[int64]map[string]string{
			1: {"En cours": "doing"},
			2: {"En cours": "done"},
		},
		faits: map[string]event.Event{},
	}
}

func (m *multiStore) ListProjects(context.Context) ([]store.Project, error) {
	return []store.Project{
		{ID: 1, Slug: "un", TrackerProject: "projet-un"},
		{ID: 2, Slug: "deux", TrackerProject: "projet-deux"},
	}, nil
}

func (m *multiStore) ResolvedPlugin(_ context.Context, projectID int64, _ string) (store.PluginSettings, error) {
	return store.PluginSettings{PluginID: "redmine", Enabled: true, Config: m.configs[projectID]}, nil
}

func (m *multiStore) StatusMapping(_ context.Context, projectID int64) (map[string]string, error) {
	return m.mappings[projectID], nil
}

func (m *multiStore) GetSyncState(_ context.Context, source string, projectID int64) (store.SyncState, error) {
	return store.SyncState{Source: source, Cursor: m.cursors[projectID]}, nil
}

func (m *multiStore) RecordSyncSuccess(_ context.Context, _ string, projectID int64, cursor string) error {
	m.cursors[projectID] = cursor
	return nil
}

func (m *multiStore) RecordSyncFailure(_ context.Context, _ string, projectID int64, message string) error {
	m.failures[projectID] = message
	return nil
}

func (m *multiStore) RecordEvent(_ context.Context, fact event.Event) (bool, error) {
	if err := fact.Validate(); err != nil {
		return false, err
	}
	if _, known := m.faits[fact.DedupKey]; known {
		return false, nil
	}
	m.faits[fact.DedupKey] = fact
	return true, nil
}

// sondeDuSuivi assemble la sonde et sa boucle sur un magasin donné.
func sondeDuSuivi(
	t *testing.T, db *multiStore, opener SecretOpener,
	connect TrackerConnector,
) (*ProbeRunner, Probe) {
	t.Helper()
	sonde := NewRedmineProbe(
		NewTrackerSync(db, connect, quietLogger()).WithNormalizer(testNames))
	return NewProbeRunner(db, opener, time.Minute, quietLogger(), sonde), sonde
}

// Chaque projet avance son propre curseur.
//
// C'est la correction du défaut : un curseur commun faisait qu'un projet reprenait
// depuis un instant où il n'avait rien lu, et sautait son historique.
func TestChaqueProjetAvanceSonPropreCurseur(t *testing.T) {
	db := newMultiStore()
	client := &fakeRedmine{versions: []provider.Version{{ID: "1", Name: "v1.0.0", Status: "open"}}}

	connect := func(TrackerConnection) (TrackerReader, error) { return client, nil }
	runner, sonde := sondeDuSuivi(t, db, openerDeTest{}, connect)
	runner.sweep(context.Background(), sonde)

	if len(db.cursors) != 2 {
		t.Fatalf("curseurs enregistrés = %v, attendu un par projet", db.cursors)
	}
	if db.cursors[1] == "" || db.cursors[2] == "" {
		t.Errorf("curseurs = %v", db.cursors)
	}
}

// Chaque projet interroge **son** serveur, avec **sa** clé descellée.
//
// Sans cela, deux clients partageraient une connexion : le second lirait les
// versions du premier, et l'écran des versions serait faux sans rien signaler.
func TestChaqueProjetInterrogeSonPropreServeur(t *testing.T) {
	db := newMultiStore()

	var vues []TrackerConnection
	connect := func(connection TrackerConnection) (TrackerReader, error) {
		vues = append(vues, connection)
		return &fakeRedmine{}, nil
	}
	runner, sonde := sondeDuSuivi(t, db, openerDeTest{}, connect)
	runner.sweep(context.Background(), sonde)

	if len(vues) != 2 {
		t.Fatalf("%d connexions, attendu 2", len(vues))
	}
	if vues[0].URL == vues[1].URL {
		t.Errorf("les deux projets visent le même serveur: %q", vues[0].URL)
	}
	// La clé est **descellée** avant d'atteindre le client. Ce n'est plus le suivi
	// qui l'ouvre mais la boucle, pour toutes les sondes — et la garantie est la
	// même : le contraire enverrait une enveloppe en guise de clé d'API, et le
	// suivi répondrait 401 sans que le message désigne le scellement.
	for _, connection := range vues {
		if strings.HasPrefix(connection.APIKey, "scellé:") {
			t.Errorf("clé encore scellée: %q", connection.APIKey)
		}
	}
	if vues[0].Project != "projet-un" || vues[1].Project != "projet-deux" {
		t.Errorf("projets visés = %q / %q", vues[0].Project, vues[1].Project)
	}
}

// L'échec d'un projet n'arrête pas les suivants, et chacun consigne le sien.
//
// Deux projets peuvent viser deux instances : la panne de l'une ne dit rien de
// l'autre, et interrompre la passe ferait qu'un client sans suivi opérationnel
// priverait tous les autres de synchronisation.
func TestLEchecDUnProjetNArretePasLesSuivants(t *testing.T) {
	db := newMultiStore()
	panne := errors.New("redmine injoignable")

	connect := func(connection TrackerConnection) (TrackerReader, error) {
		if connection.Project == "projet-un" {
			return nil, panne
		}
		return &fakeRedmine{}, nil
	}
	runner, sonde := sondeDuSuivi(t, db, openerDeTest{}, connect)
	runner.sweep(context.Background(), sonde)

	consigné, présent := db.failures[1]
	if !présent {
		t.Fatal("l'échec du premier projet n'a pas été consigné")
	}
	if !strings.Contains(consigné, "injoignable") {
		t.Errorf("motif consigné %q — il doit porter la panne", consigné)
	}
	// Le curseur du projet en panne n'avance pas : la passe suivante reverra la
	// fenêtre qu'il a ratée.
	if db.cursors[1] != "" {
		t.Errorf("curseur du projet en panne = %q, attendu inchangé", db.cursors[1])
	}
	if db.cursors[2] == "" {
		t.Error("le second projet n'a pas été synchronisé")
	}
	if _, consigné := db.failures[2]; consigné {
		t.Errorf("le second projet porte un échec: %q", db.failures[2])
	}
}

// Une clé illisible se dit comme telle, et ne cite pas la valeur.
//
// C'est le symptôme d'une rotation faite sans conserver l'ancienne clé. Le
// confondre avec une panne du suivi enverrait chercher du côté du serveur.
func TestUneCleIllisibleSeDitSansCiterLaValeur(t *testing.T) {
	db := newMultiStore()
	connect := func(TrackerConnection) (TrackerReader, error) { return &fakeRedmine{}, nil }

	runner, sonde := sondeDuSuivi(t, db,
		openerDeTest{err: errors.New("déchiffrement refusé")}, connect)
	runner.sweep(context.Background(), sonde)

	motif, présent := db.failures[1]
	if !présent {
		t.Fatal("une clé illisible doit faire échouer la passe du projet")
	}
	// Le champ est nommé : sans lui, « déchiffrement refusé » ne dirait pas quoi
	// aller regarder.
	if !strings.Contains(motif, "api_key") {
		t.Errorf("motif %q — il doit désigner le champ", motif)
	}
	if strings.Contains(motif, "clé-un") {
		t.Errorf("motif %q — il cite la valeur", motif)
	}
}

// L'identifiant du projet dans le suivi retombe sur la colonne `tracker_project`.
//
// Elle a précédé les plugins, elle est juste, et un déploiement dont l'amorçage
// n'aurait pas tourné ne doit pas cesser de se synchroniser pour autant.
func TestLIdentifiantDuSuiviRetombeSurLaColonne(t *testing.T) {
	db := newMultiStore()
	// Le réglage ne dit rien du projet : seule la colonne le sait.
	db.configs[1] = map[string]string{"url": "https://redmine.un.fr", "api_key": "scellé:clé"}

	var vues []TrackerConnection
	connect := func(connection TrackerConnection) (TrackerReader, error) {
		vues = append(vues, connection)
		return &fakeRedmine{}, nil
	}
	runner, sonde := sondeDuSuivi(t, db, openerDeTest{}, connect)
	runner.sweep(context.Background(), sonde)

	if len(vues) == 0 || vues[0].Project != "projet-un" {
		t.Errorf("projet visé = %+v", vues)
	}
}

// Un plugin actif sans identifiant de projet le dit, plutôt que de se taire.
//
// # Un comportement que la migration a changé, délibérément
//
// Avant, un projet sans suivi était simplement absent de `TrackedProjects` : la
// boucle ne le voyait pas. Maintenant la boucle voit **tous** les projets et
// s'appuie sur l'activation du plugin. Or activer Redmine sur un projet est une
// intention ; la taire laisserait un écran des versions vide sans que rien ne
// dise pourquoi.
func TestUnSuiviActifSansProjetSeDit(t *testing.T) {
	db := newMultiStore()
	db.configs[1] = map[string]string{"url": "https://redmine.un.fr", "api_key": "scellé:clé"}

	connect := func(TrackerConnection) (TrackerReader, error) { return &fakeRedmine{}, nil }
	runner, sonde := sondeDuSuivi(t, db, openerDeTest{}, connect)

	// Le projet 1 n'a plus d'identifiant nulle part : ni dans le réglage, ni dans
	// la colonne.
	projetsSansSuivi := &multiStoreSansColonne{multiStore: db}
	runner.store = projetsSansSuivi
	runner.sweep(context.Background(), sonde)

	motif, présent := db.failures[1]
	if !présent {
		t.Fatal("un plugin actif sans identifiant de projet doit être consigné")
	}
	if !strings.Contains(motif, "identifiant de projet") {
		t.Errorf("motif %q — il doit nommer ce qui manque", motif)
	}
}

// multiStoreSansColonne rend les mêmes projets, sans leur identifiant de suivi.
type multiStoreSansColonne struct{ *multiStore }

func (m *multiStoreSansColonne) ListProjects(context.Context) ([]store.Project, error) {
	return []store.Project{{ID: 1, Slug: "un"}}, nil
}
