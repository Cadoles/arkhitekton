package worker

import (
	"context"
	"errors"
	"testing"
	"time"

	dashhttp "forge.cadoles.com/vcarroy/mse-dashboard/internal/http"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/provider"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/store"
)

type fakeForge struct {
	pulls      []provider.PullRequest
	commits    map[int64][]string
	commitsErr error
	since      time.Time
}

func (f *fakeForge) ListMergedPullRequests(_ context.Context, since time.Time) ([]provider.PullRequest, error) {
	f.since = since
	return f.pulls, nil
}

func (f *fakeForge) LoadCommitMessages(_ context.Context, pull *provider.PullRequest) error {
	if f.commitsErr != nil {
		return f.commitsErr
	}
	pull.Commits = f.commits[pull.Number]
	return nil
}

type fakeNoteStore struct {
	candidates map[int64]store.NoteEntry
	linked     []int64
	sources    map[int64]string
}

func (f *fakeNoteStore) NoteCandidates(context.Context, []string) (map[int64]store.NoteEntry, error) {
	return f.candidates, nil
}

func (f *fakeNoteStore) LinkNoteIssue(
	_ context.Context, _, redmineID int64, source, pullRequest, _ string,
) error {
	for _, already := range f.linked {
		if already == redmineID {
			return store.ErrNoteAlreadyLinked
		}
	}
	f.linked = append(f.linked, redmineID)
	if f.sources == nil {
		f.sources = map[int64]string{}
	}
	f.sources[redmineID] = source + " " + pullRequest
	return nil
}

func candidates(ids ...int64) map[int64]store.NoteEntry {
	entries := map[int64]store.NoteEntry{}
	for _, id := range ids {
		entries[id] = store.NoteEntry{RedmineID: id, Known: true}
	}
	return entries
}

// Les deux conditions sont nécessaires, et c'est tout le sujet : une demande en
// revue dont rien n'est fusionné n'est pas dans l'image, et une PR fusionnée qui
// cite une demande encore en développement désigne un travail partiel.
func TestSeulesLesDemandesEnRevueCiteesParUnePRFusionneeSontRetenues(t *testing.T) {
	forge := &fakeForge{
		pulls: []provider.PullRequest{
			{Number: 10, Title: "Corrige la pagination (#1234)"},
			// Cite une demande qui n'est pas en revue : ignorée.
			{Number: 11, Title: "wip sur #9999"},
		},
	}
	notes := &fakeNoteStore{candidates: candidates(1234, 4321)}

	result, err := NewNoteBuilder(forge, notes, []string{"En revue"}, quietLogger()).
		Build(context.Background(), 7, time.Time{}, "vcarroy")
	if err != nil {
		t.Fatalf("détection: %v", err)
	}

	if len(notes.linked) != 1 || notes.linked[0] != 1234 {
		t.Errorf("demandes rattachées: %v — seule 1234 remplit les deux conditions", notes.linked)
	}
	// 4321 est en revue mais aucune PR ne la cite : elle n'est pas dans l'image.
	if result.Linked != 1 {
		t.Errorf("%d rattachements", result.Linked)
	}
}

// Le numéro peut n'être que dans un commit : le titre seul manquerait la
// demande.
func TestUneDemandeCiteeSeulementDansUnCommitEstRetenue(t *testing.T) {
	forge := &fakeForge{
		pulls:   []provider.PullRequest{{Number: 12, Title: "Corrige la pagination"}},
		commits: map[int64][]string{12: {"wip", "fix: borne l'offset\n\nRefs #1234"}},
	}
	notes := &fakeNoteStore{candidates: candidates(1234)}

	if _, err := NewNoteBuilder(forge, notes, []string{"En revue"}, quietLogger()).
		Build(context.Background(), 7, time.Time{}, "vcarroy"); err != nil {
		t.Fatalf("détection: %v", err)
	}
	if len(notes.linked) != 1 || notes.linked[0] != 1234 {
		t.Errorf("demandes rattachées: %v", notes.linked)
	}
}

// Deux PR pour une même demande — un correctif puis son suivi — ne la comptent
// qu'une fois.
func TestDeuxPullRequestsPourUneDemandeNeLaComptentQuUneFois(t *testing.T) {
	forge := &fakeForge{
		pulls: []provider.PullRequest{
			{Number: 20, Title: "Corrige #1234"},
			{Number: 21, Title: "Suite de #1234"},
		},
	}
	notes := &fakeNoteStore{candidates: candidates(1234)}

	result, err := NewNoteBuilder(forge, notes, []string{"En revue"}, quietLogger()).
		Build(context.Background(), 7, time.Time{}, "vcarroy")
	if err != nil {
		t.Fatalf("détection: %v", err)
	}
	if result.Linked != 1 || len(notes.linked) != 1 {
		t.Errorf("%d rattachements pour une demande citée deux fois", result.Linked)
	}
	// La première PR rencontrée fait foi.
	if notes.sources[1234] != "auto #20" {
		t.Errorf("source retenue: %q", notes.sources[1234])
	}
}

// Des commits illisibles ne font pas échouer la détection — le titre reste
// exploitable — mais la note devient partielle, et le dire évite de la prendre
// pour exhaustive.
func TestDesCommitsIllisiblesRendentLaNotePartielleSansLaPerdre(t *testing.T) {
	forge := &fakeForge{
		pulls:      []provider.PullRequest{{Number: 30, Title: "Corrige #1234"}},
		commitsErr: errors.New("forge injoignable"),
	}
	notes := &fakeNoteStore{candidates: candidates(1234)}

	result, err := NewNoteBuilder(forge, notes, []string{"En revue"}, quietLogger()).
		Build(context.Background(), 7, time.Time{}, "vcarroy")
	if err != nil {
		t.Fatalf("la détection ne doit pas échouer: %v", err)
	}
	if !result.Incomplete {
		t.Error("la note devrait être signalée partielle")
	}
	if result.Linked != 1 {
		t.Errorf("%d rattachements — le titre reste exploitable", result.Linked)
	}
}

// Aucune demande en revue : la forge n'est pas interrogée. Deux cents requêtes
// pour découvrir qu'il n'y a rien à rattacher se paieraient à chaque création.
func TestSansDemandeEnRevueLaForgeNEstPasInterrogee(t *testing.T) {
	forge := &fakeForge{pulls: []provider.PullRequest{{Number: 40, Title: "Corrige #1234"}}}
	notes := &fakeNoteStore{candidates: map[int64]store.NoteEntry{}}

	result, err := NewNoteBuilder(forge, notes, []string{"En revue"}, quietLogger()).
		Build(context.Background(), 7, time.Time{}, "vcarroy")
	if err != nil {
		t.Fatalf("détection: %v", err)
	}
	if result.Pulls != 0 {
		t.Errorf("%d PR examinées alors qu'aucune demande n'est éligible", result.Pulls)
	}
}

// La borne de lecture est transmise à la forge : sans elle, la détection
// remonterait des demandes livrées il y a deux ans.
func TestLaBorneDeLectureEstTransmiseALaForge(t *testing.T) {
	forge := &fakeForge{}
	notes := &fakeNoteStore{candidates: candidates(1)}
	since := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)

	if _, err := NewNoteBuilder(forge, notes, []string{"En revue"}, quietLogger()).
		Build(context.Background(), 7, since, "vcarroy"); err != nil {
		t.Fatalf("détection: %v", err)
	}
	if !forge.since.Equal(since) {
		t.Errorf("borne transmise %v, attendu %v", forge.since, since)
	}
}

// Le `kind` est écrit deux fois — ici et dans l'API qui met le job en file — pour
// éviter un cycle d'import. Deux valeurs divergentes donneraient un job que la
// file accepte et que rien ne traite : il resterait `queued` indéfiniment, sans
// erreur nulle part.
func TestLeKindDeNoteEstLeMemeDesDeuxCotes(t *testing.T) {
	if KindReleaseNotes != dashhttp.ReleaseNotesJobKind {
		t.Errorf("worker: %q, api: %q — un job mis en file ne serait jamais traité",
			KindReleaseNotes, dashhttp.ReleaseNotesJobKind)
	}
}
