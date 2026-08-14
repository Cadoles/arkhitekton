package logs

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"forge.cadoles.com/vcarroy/mse-dashboard/internal/blob"
)

// chunkWriter accumule les lignes d'une étape et les écrit par tranches.
//
// Chaque tranche est un objet complet, durable dès son écriture. C'est ce qui rend
// la capture résistante à un arrêt du worker : ce qui est écrit est acquis, et la
// reprise ne recommence qu'à partir de la dernière ligne connue.
type chunkWriter struct {
	storage *blob.Client
	target  Target
	step    Step
	// index est le rang de l'étape dans la task. Il entre dans la clé pour que
	// l'ordre lexicographique des tranches soit l'ordre du journal.
	index int

	limit    int
	buffer   bytes.Buffer
	sequence int

	// lastTimestamp borne la reprise. Il vaut, au démarrage, l'horodatage de la
	// dernière ligne déjà stockée ; ensuite, celui de la dernière ligne acceptée.
	lastTimestamp *time.Time
}

// newChunkWriter prépare l'écriture et détermine le point de reprise.
func newChunkWriter(
	ctx context.Context, storage *blob.Client, target Target, step Step, index, limit int,
) (*chunkWriter, error) {
	writer := &chunkWriter{storage: storage, target: target, step: step, index: index, limit: limit}

	prefix, err := storage.Keys.TaskLogChunkPrefix(target.RunName, target.TaskName, step.Container, index)
	if err != nil {
		return nil, err
	}

	existing, err := storage.List(ctx, prefix)
	if err != nil {
		return nil, err
	}
	if len(existing) == 0 {
		return writer, nil
	}

	keys := make([]string, 0, len(existing))
	for _, object := range existing {
		keys = append(keys, object.Key)
	}
	sortStrings(keys)

	writer.sequence = len(keys)

	// Le point de reprise se lit dans la dernière tranche, et non en base : le
	// stockage est la source de vérité de ce qui a été écrit, et une base en
	// avance sur lui ferait perdre des lignes.
	last := keys[len(keys)-1]
	timestamp, err := lastTimestampOf(ctx, storage, last)
	if err != nil {
		return nil, fmt.Errorf("lecture du point de reprise dans %q: %w", last, err)
	}
	writer.lastTimestamp = timestamp

	return writer, nil
}

// accept écarte les lignes déjà stockées.
//
// `sinceTime` de l'API Kubernetes a une précision à la seconde : il renvoie
// nécessairement des lignes déjà connues après une reprise. Les dupliquer serait
// visible dans le journal ; les écarter ici est possible parce que kubelet
// horodate chaque ligne à la nanoseconde.
//
// Une ligne sans horodatage exploitable est toujours acceptée : la perdre pour un
// défaut de format serait le mauvais arbitrage.
func (w *chunkWriter) accept(line Line) bool {
	if w.lastTimestamp == nil || line.Timestamp.IsZero() {
		return true
	}
	return line.Timestamp.After(*w.lastTimestamp)
}

// append ajoute une ligne au tampon, et écrit une tranche si le seuil est atteint.
func (w *chunkWriter) append(ctx context.Context, raw string) error {
	if !strings.HasSuffix(raw, "\n") {
		raw += "\n"
	}
	w.buffer.WriteString(raw)

	if line := parseLine(raw); !line.Timestamp.IsZero() {
		timestamp := line.Timestamp
		w.lastTimestamp = &timestamp
	}

	if w.buffer.Len() >= w.limit {
		return w.flush(ctx)
	}
	return nil
}

// flush écrit le tampon comme nouvelle tranche. Sans contenu, ne fait rien.
func (w *chunkWriter) flush(ctx context.Context) error {
	if w.buffer.Len() == 0 {
		return nil
	}

	w.sequence++
	key, err := w.storage.Keys.TaskLogChunk(
		w.target.RunName, w.target.TaskName, w.step.Container, w.index, w.sequence)
	if err != nil {
		return err
	}

	payload := w.buffer.Bytes()
	if err := w.storage.Put(ctx, key,
		bytes.NewReader(payload), int64(len(payload)), "text/plain; charset=utf-8"); err != nil {
		// Le tampon est conservé : la prochaine tentative réécrira tout. Le
		// vider ici perdrait définitivement des lignes déjà lues du pod, que
		// personne ne pourra relire.
		return err
	}

	w.buffer.Reset()
	return nil
}

// readAll lit un flux en entier. Enveloppe nommée pour garder l'import io hors du
// reste du fichier.
func readAll(reader io.Reader) ([]byte, error) { return io.ReadAll(reader) }

// lastTimestampOf rend l'horodatage de la dernière ligne d'un objet.
func lastTimestampOf(ctx context.Context, storage *blob.Client, key string) (*time.Time, error) {
	reader, err := storage.Get(ctx, key)
	if err != nil {
		return nil, err
	}
	defer reader.Close()

	// La tranche est relue entièrement. Une lecture par plage depuis la fin
	// suffirait, mais une tranche pèse au plus le seuil de flush — un mégaoctet —
	// et cela n'arrive qu'une fois par étape au démarrage.
	content, err := readAll(reader)
	if err != nil {
		return nil, err
	}

	lines := strings.Split(strings.TrimRight(string(content), "\n"), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if line := parseLine(lines[i]); !line.Timestamp.IsZero() {
			timestamp := line.Timestamp
			return &timestamp, nil
		}
	}
	// Tranche sans aucune ligne horodatée : la reprise se fera sans borne, au
	// risque de doublons. Préférable à un saut qui perdrait des lignes.
	return nil, nil
}

func sortStrings(values []string) { sort.Strings(values) }
