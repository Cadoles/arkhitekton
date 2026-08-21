package logs

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// putChunk écrit une tranche à un rang d'étape et de séquence donnés.
func putChunk(t *testing.T, target Target, index, sequence int, container, content string) {
	t.Helper()

	storage := testStorage(t)
	key, err := storage.Keys.TaskLogChunk(target.RunName, target.TaskName, container, index, sequence)
	if err != nil {
		t.Fatalf("clé: %v", err)
	}
	if err := storage.Put(context.Background(), key,
		strings.NewReader(content), int64(len(content)), "text/plain"); err != nil {
		t.Fatalf("écriture: %v", err)
	}
}

// Un journal en cours et un journal scellé se lisent de la même façon : c'est ce
// qui permet de suivre un run puis de continuer après son scellement, sans que le
// client ait à changer de mode.
func TestUnsealedLogIsReadAsOneContinuousStream(t *testing.T) {
	storage := testStorage(t)
	ctx := context.Background()

	target := uniqueTarget(t, Step{Name: "tests", Container: "step-run-tests"})
	cleanup(t, storage, target)

	putChunk(t, target, 0, 1, "step-run-tests", "AAAA")
	putChunk(t, target, 0, 2, "step-run-tests", "BBBB")
	// Étape suivante : son rang la place après, malgré un nom alphabétiquement
	// antérieur.
	putChunk(t, target, 1, 1, "step-publish-results", "CCCC")

	reader := NewReader(storage)

	segment, err := reader.From(ctx, target.RunName, target.TaskName, 0, 0)
	if err != nil {
		t.Fatalf("lecture: %v", err)
	}
	if string(segment.Content) != "AAAABBBBCCCC" {
		t.Errorf("contenu = %q", segment.Content)
	}
	if segment.Sealed {
		t.Error("un journal en tranches ne doit pas être annoncé scellé")
	}
	if segment.Size != 12 || segment.NextOffset != 12 {
		t.Errorf("taille = %d, prochain décalage = %d", segment.Size, segment.NextOffset)
	}
}

// La lecture par plage ne doit toucher que les tranches concernées : afficher la
// fin d'un journal de cinquante mégaoctets ne doit pas coûter la lecture de son
// début.
func TestTailReadsOnlyTheEnd(t *testing.T) {
	storage := testStorage(t)
	ctx := context.Background()

	target := uniqueTarget(t, Step{Name: "tests", Container: "step-run-tests"})
	cleanup(t, storage, target)

	putChunk(t, target, 0, 1, "step-run-tests", strings.Repeat("x", 1000))
	putChunk(t, target, 0, 2, "step-run-tests", "FIN-DU-JOURNAL")

	reader := NewReader(storage)
	segment, err := reader.Tail(ctx, target.RunName, target.TaskName, 14)
	if err != nil {
		t.Fatalf("lecture: %v", err)
	}

	if string(segment.Content) != "FIN-DU-JOURNAL" {
		t.Errorf("contenu = %q", segment.Content)
	}
	if segment.Offset != 1000 {
		t.Errorf("décalage = %d, attendu 1000", segment.Offset)
	}
}

// Le suivi en direct : le client redemande à partir du décalage rendu et ne
// reçoit que ce qui s'est ajouté.
func TestFromOffsetReturnsOnlyWhatWasAppended(t *testing.T) {
	storage := testStorage(t)
	ctx := context.Background()

	target := uniqueTarget(t, Step{Name: "tests", Container: "step-run-tests"})
	cleanup(t, storage, target)

	putChunk(t, target, 0, 1, "step-run-tests", "premier\n")
	reader := NewReader(storage)

	first, err := reader.From(ctx, target.RunName, target.TaskName, 0, 0)
	if err != nil {
		t.Fatalf("première lecture: %v", err)
	}

	// Rien de neuf : un segment vide, et non une erreur.
	empty, err := reader.From(ctx, target.RunName, target.TaskName, first.NextOffset, 0)
	if err != nil {
		t.Fatalf("lecture à vide: %v", err)
	}
	if len(empty.Content) != 0 {
		t.Errorf("contenu inattendu: %q", empty.Content)
	}

	putChunk(t, target, 0, 2, "step-run-tests", "second\n")

	next, err := reader.From(ctx, target.RunName, target.TaskName, first.NextOffset, 0)
	if err != nil {
		t.Fatalf("seconde lecture: %v", err)
	}
	if string(next.Content) != "second\n" {
		t.Errorf("contenu = %q, attendu la seule nouveauté", next.Content)
	}
}

// Après scellement, l'objet unique prime sur les tranches. La purge peut échouer
// sans remettre le scellement en cause : lire les deux produirait un doublon.
func TestSealedObjectWinsOverLeftoverChunks(t *testing.T) {
	storage := testStorage(t)
	ctx := context.Background()

	target := uniqueTarget(t, Step{Name: "tests", Container: "step-run-tests"})
	cleanup(t, storage, target)

	putChunk(t, target, 0, 1, "step-run-tests", "tranche oubliée\n")

	object, err := storage.Keys.TaskLogObject(target.RunName, target.TaskName)
	if err != nil {
		t.Fatalf("clé: %v", err)
	}
	const sealed = "journal scellé\n"
	if err := storage.Put(ctx, object, strings.NewReader(sealed), int64(len(sealed)), "text/plain"); err != nil {
		t.Fatalf("écriture de l'objet scellé: %v", err)
	}

	segment, err := NewReader(storage).From(ctx, target.RunName, target.TaskName, 0, 0)
	if err != nil {
		t.Fatalf("lecture: %v", err)
	}
	if string(segment.Content) != sealed {
		t.Errorf("contenu = %q, attendu l'objet scellé seul", segment.Content)
	}
	if !segment.Sealed {
		t.Error("le journal devrait être annoncé scellé")
	}
}

// Une task sans journal — jamais démarrée, ou déjà purgée — rend un segment vide,
// pas une erreur : c'est un état normal de l'interface.
func TestMissingLogIsEmptyNotAnError(t *testing.T) {
	storage := testStorage(t)
	target := uniqueTarget(t, Step{Name: "tests", Container: "step-run-tests"})

	segment, err := NewReader(storage).Tail(context.Background(), target.RunName, target.TaskName, 0)
	if err != nil {
		t.Fatalf("lecture: %v", err)
	}
	if len(segment.Content) != 0 || segment.Size != 0 || segment.Sealed {
		t.Errorf("segment = %+v", segment)
	}
}

func TestReadIsBounded(t *testing.T) {
	storage := testStorage(t)
	ctx := context.Background()

	target := uniqueTarget(t, Step{Name: "tests", Container: "step-run-tests"})
	cleanup(t, storage, target)

	putChunk(t, target, 0, 1, "step-run-tests", strings.Repeat("y", 2000))

	// Une longueur démesurée est ramenée au plafond, pas honorée : un client qui
	// demande tout d'un journal de cinquante mégaoctets ne doit pas pouvoir
	// saturer la mémoire du serveur.
	segment, err := NewReader(storage).From(ctx, target.RunName, target.TaskName, 0, MaxRead*10)
	if err != nil {
		t.Fatalf("lecture: %v", err)
	}
	if int64(len(segment.Content)) != 2000 {
		t.Errorf("%d octets lus", len(segment.Content))
	}
	if fmt.Sprint(segment.Size) != "2000" {
		t.Errorf("taille = %d", segment.Size)
	}
}

// La numérotation absolue repose là-dessus : combien de lignes complètes
// précèdent la fenêtre affichée.
//
// Sans ce compte, la visionneuse numérotait à partir de ce qu'elle avait chargé,
// et « ligne 42 » désignait deux lignes différentes de part et d'autre d'un lien
// partagé — l'un ayant lu la fin seule, l'autre le journal entier.
func TestLinesBeforeCountsAcrossChunks(t *testing.T) {
	storage := testStorage(t)
	ctx := context.Background()

	target := uniqueTarget(t, Step{Name: "tests", Container: "step-run-tests"})
	cleanup(t, storage, target)

	// Deux tranches, la coupure tombant au milieu d'une ligne : c'est le cas
	// réel, le collecteur découpe sur la taille et non sur les sauts de ligne.
	putChunk(t, target, 0, 1, "step-run-tests", "une\ndeux\ntro")
	putChunk(t, target, 0, 2, "step-run-tests", "is\nquatre\n")

	reader := NewReader(storage)

	// Un décalage nul ne coûte rien et ne précède rien.
	if count, err := reader.LinesBefore(ctx, target.RunName, target.TaskName, 0); err != nil || count != 0 {
		t.Errorf("avant le début: %d, %v", count, err)
	}

	// Position 9 : juste après « une\ndeux\n », donc deux lignes complètes.
	if count, err := reader.LinesBefore(ctx, target.RunName, target.TaskName, 9); err != nil || count != 2 {
		t.Errorf("après deux lignes: %d, %v — attendu 2", count, err)
	}

	// Position 14, au milieu de « trois » et donc dans la seconde tranche : les
	// deux mêmes lignes complètes précèdent, la troisième est entamée. C'est ce
	// qui fait qu'une fenêtre ouverte en plein milieu d'une ligne la numérote
	// comme la ligne à laquelle elle appartient.
	if count, err := reader.LinesBefore(ctx, target.RunName, target.TaskName, 14); err != nil || count != 2 {
		t.Errorf("au milieu d'une ligne: %d, %v — attendu 2", count, err)
	}

	// La fin : toutes les lignes précèdent.
	if count, err := reader.LinesBefore(ctx, target.RunName, target.TaskName, 22); err != nil || count != 4 {
		t.Errorf("à la fin: %d, %v — attendu 4", count, err)
	}
}
