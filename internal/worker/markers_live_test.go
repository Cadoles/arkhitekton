package worker

import (
	"context"
	"os"
	"strings"
	"testing"

	"forge.cadoles.com/vcarroy/mse-dashboard/internal/blob"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/config"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/logs"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/results"
)

/*
 * Vérification du canal de marqueurs contre un journal **réellement scellé**,
 * gardée par `MARKERS_LIVE=1` comme les vérifications de Redmine et de la forge.
 *
 * Les tests unitaires du canal servent un journal depuis une map : ils éprouvent
 * la logique et rien de plus. Ce qu'ils ne peuvent pas dire, c'est si le lecteur
 * rend bien le texte complet d'un objet réel — un journal scellé est écrit par
 * tranches, puis recomposé, et c'est exactement le genre d'assemblage qui marche
 * sur une fixture et pas sur la donnée.
 *
 * Ce que ce test couvre, et que rien d'autre ne couvrait : la chaîne
 * MinIO → logs.Reader → MarkerIngester sur un run que le dashboard a lui-même
 * ordonnancé, collecté et scellé.
 *
 * Variables attendues :
 *
 *   MARKERS_LIVE=1
 *   MINIO_ENDPOINT, MINIO_ACCESS_KEY, MINIO_SECRET_KEY, MINIO_BUCKET
 *   MARKERS_LIVE_RUN   nom du PipelineRun
 *   MARKERS_LIVE_TASKS noms des tasks, séparés par des virgules
 *   MARKER_TOKEN       jeton du déploiement observé (`MSE` ici)
 */

func liveMarkerSource(t *testing.T) (*logs.Reader, string, []string) {
	t.Helper()

	if os.Getenv("MARKERS_LIVE") != "1" {
		t.Skip("MARKERS_LIVE != 1: vérification contre un journal réel ignorée")
	}

	run := os.Getenv("MARKERS_LIVE_RUN")
	tasks := strings.Split(os.Getenv("MARKERS_LIVE_TASKS"), ",")
	if run == "" || len(tasks) == 0 || tasks[0] == "" {
		t.Skip("MARKERS_LIVE_RUN ou MARKERS_LIVE_TASKS non renseigné")
	}

	storage, err := blob.Open(context.Background(), config.StorageConfig{
		Endpoint:  os.Getenv("MINIO_ENDPOINT"),
		Bucket:    os.Getenv("MINIO_BUCKET"),
		AccessKey: os.Getenv("MINIO_ACCESS_KEY"),
		SecretKey: os.Getenv("MINIO_SECRET_KEY"),
		Region:    "NUO-Region",
		UseSSL:    os.Getenv("MINIO_USE_SSL") == "true",
	})
	if err != nil {
		t.Fatalf("stockage objet: %v", err)
	}
	return logs.NewReader(storage), run, tasks
}

// Le journal scellé d'un run réel doit rendre ses descripteurs, entiers et
// analysables.
//
// L'assertion porte sur ce qui est **lu**, pas sur ce qui est enregistré : un run
// dont tous les outils ont conclu n'enregistre rien, et ne prouverait donc rien
// du chemin de lecture.
func TestLiveLesDescripteursDunRunScelleSeLisent(t *testing.T) {
	reader, run, tasks := liveMarkerSource(t)

	set := results.NewMarkerSet(os.Getenv("MARKER_TOKEN"))
	ingester := NewMarkerIngester(&fakeMarkerStore{existing: map[string]bool{}}, reader, set, quietLogger())

	found := map[string]results.Meta{}
	var read int
	for _, task := range tasks {
		task = strings.TrimSpace(task)
		text, err := ingester.readTask(context.Background(), run, task)
		if err != nil {
			t.Fatalf("lecture de %s/%s: %v", run, task, err)
		}
		read += len(text)
		// Le début du journal est rendu quoi qu'il arrive : quand aucun
		// descripteur n'est trouvé, la question suivante est toujours « qu'y
		// avait-il alors dans ce journal », et l'échec doit y répondre seul.
		t.Logf("%s: %d octets, début: %s", task, len(text), firstLines(text, 3))
		// Même retrait d'horodatage que l'ingesteur : sans lui, ce test passerait
		// à côté de descripteurs pourtant présents — c'est exactement le défaut
		// qu'il a servi à trouver.
		for tool, meta := range set.ScanMarkers(logs.Plain(text)).Metas {
			found[tool] = meta
		}
	}

	if read == 0 {
		t.Fatal("aucun octet lu: le lecteur ne rend rien du journal scellé")
	}
	if len(found) == 0 {
		t.Fatalf("%d octets lus, aucun descripteur trouvé: soit le jeton %q ne correspond pas "+
			"à ce que les Tasks émettent, soit le journal est tronqué", read, set.Meta)
	}

	for tool, meta := range found {
		t.Logf("descripteur lu: outil=%s statut=%s code=%d", tool, meta.Status, meta.ExitCode)
		if tool == "" {
			t.Error("un descripteur sans outil ne peut être rattaché à rien")
		}
	}
}

// Et le pendant, qui est la propriété la plus facile à casser sans s'en
// apercevoir : un run dont tous les outils ont conclu ne doit enregistrer aucun
// résultat. Un faux positif ici marquerait « non mesuré » une famille qui l'a
// été, et sortirait du score un signal parfaitement valide.
func TestLiveUnRunSainNenregistreRien(t *testing.T) {
	reader, run, tasks := liveMarkerSource(t)

	set := results.NewMarkerSet(os.Getenv("MARKER_TOKEN"))
	db := &fakeMarkerStore{existing: map[string]bool{}}
	ingester := NewMarkerIngester(db, reader, set, quietLogger())

	trimmed := make([]string, 0, len(tasks))
	for _, task := range tasks {
		trimmed = append(trimmed, strings.TrimSpace(task))
	}

	err := ingester.Ingest(context.Background(), MarkerRun{
		ID: -1, Name: run, Tasks: trimmed, Family: "static",
	})
	if err != nil {
		t.Fatalf("ingestion: %v", err)
	}

	for _, saved := range db.saved {
		// Un enregistrement n'est une faute que si l'outil avait conclu. Le test
		// est lancé sur un run réussi, donc la liste doit être vide ; si elle ne
		// l'est pas, le journal dit lequel et pourquoi.
		t.Errorf("résultat enregistré pour un run sain: outil=%s statut=%s résumé=%q",
			saved.Tool, saved.Status, saved.Summary)
	}
}

// firstLines rend les premières lignes d'un texte, pour le diagnostic.
func firstLines(text string, count int) string {
	lines := strings.SplitN(strings.TrimSpace(text), "\n", count+1)
	if len(lines) > count {
		lines = lines[:count]
	}
	return strings.Join(lines, " | ")
}
