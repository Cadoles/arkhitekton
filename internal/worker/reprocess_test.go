package worker

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"forge.cadoles.com/vcarroy/mse-dashboard/internal/store"
)

type fakeReprocessStore struct {
	targets []store.ReprocessTarget
	deleted []int64
	err     error
}

func (f *fakeReprocessStore) ListReprocessTargets(context.Context, int64) ([]store.ReprocessTarget, error) {
	return f.targets, f.err
}

func (f *fakeReprocessStore) DeleteTestResults(_ context.Context, runID int64) (int64, error) {
	f.deleted = append(f.deleted, runID)
	return 2, nil
}

func newReprocessor(targets *fakeReprocessStore, objects fakeObjects) (*fakeTestStore, *Reprocessor) {
	tests := &fakeTestStore{known: map[string]bool{}}
	discard := slog.New(slog.DiscardHandler)
	return tests, NewReprocessor(targets,
		NewTestIngester(tests, objects, discard), nil, nil, discard)
}

// Le rejeu efface avant de reconstruire. Empiler dupliquerait chaque cas et
// fausserait toute comparaison inter-release — précisément ce que le rejeu sert
// à corriger.
func TestReprocessDeletesBeforeRebuilding(t *testing.T) {
	targets := &fakeReprocessStore{targets: []store.ReprocessTarget{
		{RunID: 1, RunName: "unit-1", Tools: []string{"phpunit"}},
	}}
	objects := fakeObjects{"raw/unit-1/junit.xml": junitXML}
	tests, reprocessor := newReprocessor(targets, objects)

	report, err := reprocessor.Run(context.Background(), ReprocessOptions{})
	if err != nil {
		t.Fatalf("rejeu: %v", err)
	}

	if len(targets.deleted) != 1 || targets.deleted[0] != 1 {
		t.Errorf("effacements = %v", targets.deleted)
	}
	if len(tests.saved) != 1 {
		t.Fatalf("%d rapport(s) reconstruit(s)", len(tests.saved))
	}
	if report.Replayed != 1 || report.Deleted != 2 {
		t.Errorf("rapport = %+v", report)
	}
}

// Le rejeu détruit avant de reconstruire : pouvoir constater ce qui
// disparaîtrait, sans rien détruire, est ce qui rend l'opération défendable.
func TestDryRunTouchesNothing(t *testing.T) {
	targets := &fakeReprocessStore{targets: []store.ReprocessTarget{
		{RunID: 1, RunName: "unit-1", Tools: []string{"phpunit"}},
	}}
	tests, reprocessor := newReprocessor(targets, fakeObjects{"raw/unit-1/junit.xml": junitXML})

	report, err := reprocessor.Run(context.Background(), ReprocessOptions{DryRun: true})
	if err != nil {
		t.Fatalf("simulation: %v", err)
	}

	if len(targets.deleted) != 0 {
		t.Error("la simulation ne doit rien effacer")
	}
	if len(tests.saved) != 0 {
		t.Error("la simulation ne doit rien reconstruire")
	}
	// Elle rend compte de ce qu'elle aurait touché.
	if report.Examined != 1 || report.Replayed != 0 {
		t.Errorf("rapport = %+v", report)
	}
}

// Rejouer cinquante runs et tout perdre parce que le troisième a un artefact
// purgé serait une régression pire que le défaut d'origine.
func TestAFailingRunDoesNotStopTheOthers(t *testing.T) {
	targets := &fakeReprocessStore{targets: []store.ReprocessTarget{
		{RunID: 1, RunName: "purge", Tools: []string{"phpunit"}},
		{RunID: 2, RunName: "unit-2", Tools: []string{"phpunit"}},
	}}
	// `purge` n'a plus d'artefact lisible : son objet existe dans la liste mais
	// sa lecture échoue.
	objects := fakeObjects{
		"raw/purge/junit.xml":  "",
		"raw/unit-2/junit.xml": junitXML,
	}
	tests := &fakeTestStore{known: map[string]bool{}}
	discard := slog.New(slog.DiscardHandler)
	reprocessor := NewReprocessor(targets,
		NewTestIngester(tests, brokenObjects{objects, "raw/purge/junit.xml"}, discard),
		nil, nil, discard)

	report, err := reprocessor.Run(context.Background(), ReprocessOptions{})
	if err != nil {
		t.Fatalf("rejeu: %v", err)
	}

	if report.Replayed != 1 {
		t.Errorf("%d run(s) rejoué(s), attendu 1", report.Replayed)
	}
	// Les échecs sont nommés, pas comptés : savoir lesquels est ce qui permet de
	// décider s'il faut s'en inquiéter.
	if len(report.Failed) != 1 || report.Failed[0] != "purge" {
		t.Errorf("échecs = %v", report.Failed)
	}
	if len(tests.saved) != 1 {
		t.Errorf("%d rapport(s) reconstruit(s)", len(tests.saved))
	}
}

// brokenObjects fait échouer la lecture d'une clé précise, comme le ferait un
// objet purgé par la rétention.
type brokenObjects struct {
	fakeObjects
	broken string
}

func (b brokenObjects) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	if key == b.broken {
		return nil, errors.New("objet purgé")
	}
	return b.fakeObjects.Get(ctx, key)
}

func TestReprocessWithNothingToReplay(t *testing.T) {
	targets := &fakeReprocessStore{}
	_, reprocessor := newReprocessor(targets, fakeObjects{})

	report, err := reprocessor.Run(context.Background(), ReprocessOptions{})
	if err != nil {
		t.Fatalf("rejeu: %v", err)
	}
	if report.Examined != 0 || report.Replayed != 0 {
		t.Errorf("rapport = %+v", report)
	}
}
