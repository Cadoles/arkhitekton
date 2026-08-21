package http

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"forge.cadoles.com/vcarroy/mse-dashboard/internal/logs"
)

/*
 * Le journal en texte : ce qui sert la vue brute et le téléchargement.
 *
 * Le point qui mérite un test est la pagination. Le lecteur borne chaque appel,
 * et un journal d'intégration dépasse largement cette borne : servir la première
 * tranche et s'arrêter donnerait un fichier tronqué sans que rien ne le signale.
 */

// chunkedReader rend le journal par tranches, comme le vrai lecteur borné.
type chunkedReader struct {
	content []byte
	chunk   int64
	// calls compte les lectures : c'est ce qui distingue une pagination réelle
	// d'un coup de chance où tout tiendrait dans le premier appel.
	calls int
	err   error
}

func (r *chunkedReader) Tail(context.Context, string, string, int64) (logs.Segment, error) {
	return logs.Segment{}, errors.New("Tail n'a pas à servir le texte brut")
}

// LinesBefore ne sert pas la vue brute — elle rend le journal tel quel, sans
// numéroter — mais l'interface l'exige.
func (r *chunkedReader) LinesBefore(_ context.Context, _, _ string, offset int64) (int64, error) {
	if r.err != nil {
		return 0, r.err
	}
	if offset <= 0 || offset > int64(len(r.content)) {
		return 0, nil
	}
	return int64(bytes.Count(r.content[:offset], []byte{'\n'})), nil
}

func (r *chunkedReader) From(_ context.Context, _, _ string, offset, _ int64) (logs.Segment, error) {
	if r.err != nil {
		return logs.Segment{}, r.err
	}
	r.calls++

	size := int64(len(r.content))
	if offset > size {
		offset = size
	}
	end := offset + r.chunk
	if end > size {
		end = size
	}
	return logs.Segment{
		Content: r.content[offset:end], Offset: offset, NextOffset: end, Size: size, Sealed: true,
	}, nil
}

func rawRequest(t *testing.T, reader LogReader, query string) *httptest.ResponseRecorder {
	t.Helper()

	request := httptest.NewRequest(http.MethodGet, "/runs/mse-run-1/tasks/git-clone/log.txt"+query, nil)
	request.SetPathValue("run", "mse-run-1")
	request.SetPathValue("task", "git-clone")

	recorder := httptest.NewRecorder()
	NewLogsHandler(reader).Raw(recorder, request)
	return recorder
}

// Un journal plus long qu'une lecture unitaire doit sortir entier.
func TestLeJournalBrutEstServiEnEntier(t *testing.T) {
	reader := &chunkedReader{content: []byte("une ligne\ndeux lignes\ntrois lignes\n"), chunk: 8}

	recorder := rawRequest(t, reader, "")

	if recorder.Code != http.StatusOK {
		t.Fatalf("code %d", recorder.Code)
	}
	if got := recorder.Body.String(); got != string(reader.content) {
		t.Errorf("corps tronqué: %q", got)
	}
	if reader.calls < 2 {
		t.Errorf("%d lecture(s) — le test ne prouve rien sans pagination", reader.calls)
	}
	if got := recorder.Header().Get("Content-Type"); got != "text/plain; charset=utf-8" {
		t.Errorf("type: %q", got)
	}
	// Sans `download`, la réponse s'affiche dans l'onglet : une disposition en
	// pièce jointe la ferait enregistrer, ce qui est l'autre usage.
	if got := recorder.Header().Get("Content-Disposition"); got != "" {
		t.Errorf("disposition posée sans demande: %q", got)
	}
}

// `download` fait basculer la même ressource en fichier, nommé des deux
// identifiants : un dossier où dix fichiers s'appellent `log.txt` ne sert à rien.
func TestLeTelechargementNommeLeFichier(t *testing.T) {
	recorder := rawRequest(t, &chunkedReader{content: []byte("bonjour\n"), chunk: 64}, "?download=1")

	want := `attachment; filename="mse-run-1-git-clone.txt"`
	if got := recorder.Header().Get("Content-Disposition"); got != want {
		t.Errorf("disposition: %q, attendu %q", got, want)
	}
}

// Un journal vide n'est pas une erreur, et la boucle doit s'arrêter dessus.
func TestUnJournalVideNeBouclePas(t *testing.T) {
	recorder := rawRequest(t, &chunkedReader{content: nil, chunk: 64}, "")

	if recorder.Code != http.StatusOK || recorder.Body.Len() != 0 {
		t.Errorf("code %d, %d octet(s)", recorder.Code, recorder.Body.Len())
	}
}

// Un échec à la première lecture donne un code d'erreur : c'est le seul moment
// où il en reste un à donner, l'en-tête n'étant pas encore parti.
func TestUnEchecDeLectureRendUneErreur(t *testing.T) {
	recorder := rawRequest(t, &chunkedReader{err: errors.New("minio injoignable")}, "")

	if recorder.Code != http.StatusInternalServerError {
		t.Errorf("code %d", recorder.Code)
	}
}

// Le nom de fichier ne garde que ce qui traverse un en-tête sans dommage.
func TestLeNomDeFichierEstAssaini(t *testing.T) {
	if got := sanitizeFilename(`mse "run"/1`); got != "mse--run--1" {
		t.Errorf("nom: %q", got)
	}
}
