package worker

import (
	"context"
	"errors"
	"strings"
	"testing"

	"forge.cadoles.com/vcarroy/mse-dashboard/internal/provider"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/store"
)

/*
 * La synchronisation multi-projet.
 *
 * Le défaut qu'elle corrige était documenté et assumé : un curseur unique avançait
 * sur le premier projet lu, et le second reprenait depuis un instant où **il**
 * n'avait rien lu. Son historique était sauté, définitivement, sans message.
 */

// multiStore rend deux projets suivis et retient ce que chacun a écrit.
type multiStore struct {
	fakeRedmineStore
	// configs porte la connexion résolue de chaque projet.
	configs map[int64]map[string]string
	// cursors retient le curseur enregistré par projet, et c'est le cœur du sujet.
	cursors  map[int64]string
	failures map[int64]string
	mappings map[int64]map[string]string
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
	}
}

func (m *multiStore) TrackedProjects(context.Context) ([]store.TrackedProject, error) {
	return []store.TrackedProject{
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

func (m *multiStore) RecordSyncSuccess(_ context.Context, _ string, projectID int64, cursor string) error {
	m.cursors[projectID] = cursor
	return nil
}

func (m *multiStore) RecordSyncFailure(_ context.Context, _ string, projectID int64, message string) error {
	m.failures[projectID] = message
	return nil
}

// Chaque projet avance son propre curseur.
//
// C'est la correction du défaut : un curseur commun faisait qu'un projet reprenait
// depuis un instant où il n'avait rien lu, et sautait son historique.
func TestChaqueProjetAvanceSonPropreCurseur(t *testing.T) {
	db := newMultiStore()
	client := &fakeRedmine{versions: []provider.Version{{ID: "1", Name: "v1.0.0", Status: "open"}}}

	connect := func(TrackerConnection) (TrackerReader, error) { return client, nil }
	sync := NewTrackerSync(db, connect, openerDeTest{}, quietLogger()).WithNormalizer(testNames)

	if err := sync.Sync(context.Background()); err != nil {
		t.Fatalf("synchronisation: %v", err)
	}

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
	sync := NewTrackerSync(db, connect, openerDeTest{}, quietLogger()).WithNormalizer(testNames)

	if err := sync.Sync(context.Background()); err != nil {
		t.Fatalf("synchronisation: %v", err)
	}

	if len(vues) != 2 {
		t.Fatalf("%d connexions, attendu 2", len(vues))
	}
	if vues[0].URL == vues[1].URL {
		t.Errorf("les deux projets visent le même serveur: %q", vues[0].URL)
	}
	// La clé est **descellée** avant d'atteindre le client : le contraire
	// enverrait une enveloppe en guise de clé d'API, et le suivi répondrait 401
	// sans que le message désigne le scellement.
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
	sync := NewTrackerSync(db, connect, openerDeTest{}, quietLogger()).WithNormalizer(testNames)

	err := sync.Sync(context.Background())
	if err == nil {
		t.Fatal("l'échec doit remonter")
	}
	// Le message nomme le projet : sans lui, « redmine injoignable » ne dirait pas
	// lequel des deux.
	if !strings.Contains(err.Error(), "un") {
		t.Errorf("message %q — il doit nommer le projet en échec", err)
	}

	if _, consigné := db.failures[1]; !consigné {
		t.Error("l'échec du premier projet n'a pas été consigné")
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

	sync := NewTrackerSync(db, connect,
		openerDeTest{err: errors.New("déchiffrement refusé")}, quietLogger())

	err := sync.Sync(context.Background())
	if err == nil {
		t.Fatal("une clé illisible doit faire échouer la passe")
	}
	if !strings.Contains(err.Error(), "clé d'API") {
		t.Errorf("message %q — il doit désigner la clé", err)
	}
	if strings.Contains(err.Error(), "clé-un") {
		t.Errorf("message %q — il cite la valeur", err)
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
	sync := NewTrackerSync(db, connect, openerDeTest{}, quietLogger()).WithNormalizer(testNames)

	if err := sync.Sync(context.Background()); err != nil {
		t.Fatalf("synchronisation: %v", err)
	}
	if len(vues) == 0 || vues[0].Project != "projet-un" {
		t.Errorf("projet visé = %+v", vues)
	}
}
