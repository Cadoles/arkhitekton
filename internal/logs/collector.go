// Package logs capte les journaux des pods Tekton avant qu'ils disparaissent.
//
// Les logs Tekton sont les logs des pods : ils s'évanouissent avec eux, et le pod
// d'une task est supprimé peu après sa fin. Les lire à la demande ne marche donc
// que quelques minutes. Il faut les capter **pendant** l'exécution
// (doc 06 §6.5).
//
// Deux écarts au document valent d'être signalés, tous deux imposés par S3 :
//
//  1. **Pas d'envoi multipart.** Une partie d'envoi multipart doit peser au moins
//     5 Mio, ce qui interdit la granularité de flush utile — un incident perdrait
//     jusqu'à 5 Mio de journal. Chaque flush écrit donc un objet complet, durable
//     immédiatement, et le scellement les concatène.
//  2. **Une goroutine par étape, et non un flux unique.** Les étapes d'un TaskRun
//     démarrent l'une après l'autre, mais une étape non démarrée fait échouer la
//     lecture de son conteneur : suivre les étapes en série demanderait d'attendre
//     et de réessayer sur chacune. Chaque étape écrit dans son propre espace de
//     tranches, et l'ordre est rétabli au scellement — il est connu d'avance.
package logs

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"time"

	"forge.cadoles.com/vcarroy/mse-dashboard/internal/blob"
)

// Défauts de flush, repris de la doc 06 §6.5.
const (
	DefaultFlushInterval = 5 * time.Second
	DefaultFlushSize     = 1 << 20 // 1 Mio
)

// retryDelay sépare deux tentatives de lecture d'un conteneur pas encore démarré.
const retryDelay = 2 * time.Second

// drainGrace borne l'attente, au scellement, que les lectures en cours atteignent
// la fin du flux.
//
// Le délai existe parce qu'un TaskRun devient terminal avant que le flux de logs
// soit épuisé côté client : couper à cet instant tronquerait la fin du journal,
// c'est-à-dire précisément le message d'échec qu'on vient chercher.
const drainGrace = 30 * time.Second

// Pods lit les journaux. L'interface existe pour que le collecteur soit testable
// sans cluster : ce qu'on veut éprouver — le découpage, la reprise, le
// scellement — n'a rien de kubernetes.
type Pods interface {
	// Stream ouvre le journal d'un conteneur. `since` non nul reprend à partir de
	// cet instant. L'appelant referme.
	Stream(ctx context.Context, pod, container string, since *time.Time) (io.ReadCloser, error)
}

// Sink reçoit les lignes au fil de l'eau, pour le direct.
//
// Aucune persistance à ce niveau : un client qui se branche en cours de route
// reçoit d'abord les tranches déjà écrites, puis ce flux.
type Sink interface {
	Publish(runName, taskName string, line Line)
}

// Line est une ligne de journal horodatée par kubelet.
type Line struct {
	Timestamp time.Time
	Text      string
}

// Store est ce que le collecteur écrit en base à la fin.
type Store interface {
	SealTaskLog(ctx context.Context, runName, taskName, object string) error
}

// Step désigne une étape et son conteneur.
type Step struct {
	Name      string
	Container string
}

// Target décrit la task à suivre.
type Target struct {
	RunName  string
	TaskName string
	PodName  string
	Steps    []Step
}

func (t Target) key() string { return t.RunName + "/" + t.TaskName }

// Collector suit les tasks en cours et scelle leurs journaux.
type Collector struct {
	pods    Pods
	storage *blob.Client
	store   Store
	live    Sink
	logger  *slog.Logger

	flushInterval time.Duration
	flushSize     int

	mu     sync.Mutex
	active map[string]*collection
}

type collection struct {
	cancel context.CancelFunc
	// drain est fermé pour dire « plus de nouvelle tentative ». Les lectures en
	// cours continuent jusqu'à la fin du flux : c'est la différence entre un
	// journal complet et un journal tronqué à la dernière seconde.
	drain chan struct{}
	done  chan struct{}
}

// New assemble le collecteur.
func New(pods Pods, storage *blob.Client, store Store, live Sink, logger *slog.Logger) *Collector {
	if live == nil {
		live = discardSink{}
	}
	return &Collector{
		pods:          pods,
		storage:       storage,
		store:         store,
		live:          live,
		logger:        logger,
		flushInterval: DefaultFlushInterval,
		flushSize:     DefaultFlushSize,
		active:        make(map[string]*collection),
	}
}

// Start lance la capture d'une task, si elle n'est pas déjà en cours.
//
// L'idempotence n'est pas un raffinement : l'informer répète l'événement de mise à
// jour à chaque changement de statut d'étape, et une capture par événement
// ouvrirait des dizaines de flux sur le même conteneur.
func (c *Collector) Start(ctx context.Context, target Target) {
	if target.PodName == "" || len(target.Steps) == 0 {
		// Sans pod, il n'y a rien à lire. L'événement suivant l'apportera.
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if _, running := c.active[target.key()]; running {
		return
	}

	// Contexte détaché de celui de l'événement : la capture survit à l'événement
	// qui l'a déclenchée, et ne s'arrête qu'avec le collecteur ou la task.
	captureCtx, cancel := context.WithCancel(ctx)
	entry := &collection{cancel: cancel, drain: make(chan struct{}), done: make(chan struct{})}
	c.active[target.key()] = entry

	c.logger.Info("capture de journal démarrée",
		"run", target.RunName, "task", target.TaskName, "pod", target.PodName, "etapes", len(target.Steps))

	go func() {
		defer close(entry.done)
		c.capture(captureCtx, entry.drain, target)
	}()
}

// Finish arrête la capture d'une task et scelle son journal.
//
// Appelé quand le TaskRun devient terminal. Le scellement attend la fin des
// goroutines : sceller pendant qu'une étape écrit encore produirait un journal
// tronqué, et c'est irréversible puisque les tranches sont ensuite purgées.
func (c *Collector) Finish(ctx context.Context, target Target) error {
	c.mu.Lock()
	entry, running := c.active[target.key()]
	if running {
		delete(c.active, target.key())
	}
	c.mu.Unlock()

	if running {
		// Les lectures en cours vont jusqu'au bout du flux ; seules les nouvelles
		// tentatives cessent. Le conteneur étant terminé, l'EOF arrive dans la
		// seconde — le délai de grâce couvre le cas où le flux tarde.
		close(entry.drain)

		select {
		case <-entry.done:
		case <-time.After(drainGrace):
			c.logger.Warn("délai de grâce dépassé, capture interrompue",
				"run", target.RunName, "task", target.TaskName)
			entry.cancel()
			<-entry.done
		case <-ctx.Done():
			entry.cancel()
			return ctx.Err()
		}
	}

	return c.seal(ctx, target)
}

// Stop interrompt toutes les captures sans sceller : c'est l'arrêt du worker, pas
// la fin des tasks. Les tranches déjà écrites restent, et la reprise s'appuiera
// dessus.
func (c *Collector) Stop() {
	c.mu.Lock()
	entries := make([]*collection, 0, len(c.active))
	for key, entry := range c.active {
		entries = append(entries, entry)
		delete(c.active, key)
	}
	c.mu.Unlock()

	for _, entry := range entries {
		entry.cancel()
	}
	for _, entry := range entries {
		<-entry.done
	}
}

func (c *Collector) capture(ctx context.Context, drain <-chan struct{}, target Target) {
	var wg sync.WaitGroup
	for index, step := range target.Steps {
		wg.Add(1)
		go func(index int, step Step) {
			defer wg.Done()
			c.captureStep(ctx, drain, target, step, index)
		}(index, step)
	}
	wg.Wait()
}

// captureStep suit une étape jusqu'à la fin de son conteneur.
func (c *Collector) captureStep(
	ctx context.Context, drain <-chan struct{}, target Target, step Step, index int,
) {
	writer, err := newChunkWriter(ctx, c.storage, target, step, index, c.flushSize)
	if err != nil {
		c.logger.Error("préparation des tranches", "task", target.TaskName, "etape", step.Name, "error", err)
		return
	}

	// Reprise : le dernier horodatage déjà écrit borne à la fois la demande au
	// kubelet et le filtrage local. `sinceTime` a une précision à la seconde, il
	// renvoie donc des lignes déjà connues — mieux vaut en dupliquer que d'en
	// perdre, et le filtre les écarte.
	since := writer.lastTimestamp
	if since != nil {
		c.logger.Info("reprise de capture",
			"task", target.TaskName, "etape", step.Name, "depuis", since.Format(time.RFC3339))
	}

	for {
		if ctx.Err() != nil {
			c.flushQuietly(ctx, writer, target, step)
			return
		}

		stream, err := c.pods.Stream(ctx, target.PodName, step.Container, since)
		if err != nil {
			// Cas courant, pas une anomalie : l'étape n'a pas encore démarré. Les
			// étapes d'un TaskRun s'exécutent en série, et la dernière peut
			// attendre plusieurs minutes.
			if ctx.Err() != nil {
				c.flushQuietly(ctx, writer, target, step)
				return
			}
			select {
			case <-ctx.Done():
				c.flushQuietly(ctx, writer, target, step)
				return
			case <-drain:
				// La task est terminée et ce conteneur n'a jamais rien donné —
				// étape ignorée par Tekton, ou pod déjà supprimé. Insister
				// n'apporterait rien.
				c.flushQuietly(ctx, writer, target, step)
				return
			case <-time.After(retryDelay):
				continue
			}
		}

		lastRead, err := c.consume(ctx, stream, writer, target, step)
		_ = stream.Close()

		if lastRead != nil {
			since = lastRead
		}

		// EOF signifie conteneur terminé : le journal est complet. Toute autre
		// erreur — flux coupé, connexion perdue — justifie une reprise.
		if err == nil || errors.Is(err, io.EOF) {
			c.flushQuietly(ctx, writer, target, step)
			return
		}
		if ctx.Err() != nil {
			c.flushQuietly(ctx, writer, target, step)
			return
		}

		c.logger.Warn("flux de journal interrompu, reprise",
			"task", target.TaskName, "etape", step.Name, "error", err)
		if err := writer.flush(ctx); err != nil {
			c.logger.Error("écriture d'une tranche", "task", target.TaskName, "error", err)
		}

		select {
		case <-ctx.Done():
			return
		case <-drain:
			return
		case <-time.After(retryDelay):
		}
	}
}

// consume lit le flux ligne à ligne et rend le dernier horodatage vu.
func (c *Collector) consume(
	ctx context.Context, stream io.Reader, writer *chunkWriter, target Target, step Step,
) (*time.Time, error) {
	reader := bufio.NewReaderSize(stream, 64<<10)
	ticker := time.NewTicker(c.flushInterval)
	defer ticker.Stop()

	// Le flush périodique est porté par une goroutine : la lecture est bloquante,
	// et un pipeline silencieux pendant deux minutes ne doit pas retarder d'autant
	// la durabilité de ce qui précède.
	flushDone := make(chan struct{})
	defer func() { close(flushDone) }()
	go func() {
		for {
			select {
			case <-flushDone:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := writer.flush(ctx); err != nil {
					c.logger.Error("écriture périodique d'une tranche",
						"task", target.TaskName, "etape", step.Name, "error", err)
				}
			}
		}
	}()

	var last *time.Time
	for {
		raw, err := reader.ReadString('\n')
		if raw != "" {
			line := parseLine(raw)
			if !writer.accept(line) {
				// Ligne déjà connue, renvoyée par `sinceTime` : écartée sans
				// bruit, c'est le prix de ne rien perdre.
				continue
			}
			timestamp := line.Timestamp
			last = &timestamp

			if err := writer.append(ctx, raw); err != nil {
				return last, err
			}
			c.live.Publish(target.RunName, target.TaskName, line)
		}
		if err != nil {
			return last, err
		}
	}
}

func (c *Collector) flushQuietly(ctx context.Context, writer *chunkWriter, target Target, step Step) {
	// Contexte propre : celui de la capture est peut-être déjà annulé, et la
	// dernière tranche est justement celle qu'il ne faut pas perdre.
	flushCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()

	if err := writer.flush(flushCtx); err != nil {
		c.logger.Error("écriture de la dernière tranche",
			"task", target.TaskName, "etape", step.Name, "error", err)
	}
}

// Plain rend le texte d'un journal stocké, débarrassé de ses horodatages.
//
// Les tranches stockées **conservent** le préfixe RFC3339Nano ajouté par kubelet :
// c'est lui qui rend la reprise possible après un redémarrage du collecteur. Tout
// lecteur qui cherche autre chose qu'un affichage doit donc le retirer d'abord.
//
// Écrit ici et non chez l'appelant parce que le format appartient à ce paquet.
// L'oublier ne casse rien de visible : les lignes se lisent parfaitement, et
// seule une recherche en début de ligne échoue — silencieusement. C'est ce qui
// est arrivé au canal de marqueurs, dont chaque descripteur était précédé d'un
// horodatage et n'a donc jamais été reconnu.
func Plain(stored string) string {
	lines := strings.Split(stored, "\n")
	for index, line := range lines {
		lines[index] = parseLine(line).Text
	}
	return strings.Join(lines, "\n")
}

// parseLine sépare l'horodatage ajouté par kubelet du texte.
//
// `timestamps=true` préfixe chaque ligne d'un RFC3339Nano suivi d'un espace. Le
// préfixe est conservé dans la tranche stockée — c'est lui qui rend la reprise
// possible — et retiré pour le direct, où il serait du bruit.
func parseLine(raw string) Line {
	trimmed := strings.TrimRight(raw, "\r\n")

	prefix, text, found := strings.Cut(trimmed, " ")
	if !found {
		return Line{Text: trimmed}
	}
	timestamp, err := time.Parse(time.RFC3339Nano, prefix)
	if err != nil {
		// Ligne sans horodatage exploitable : conservée telle quelle. Perdre une
		// ligne parce qu'elle est mal formée serait le pire des arbitrages.
		return Line{Text: trimmed}
	}
	return Line{Timestamp: timestamp, Text: text}
}

type discardSink struct{}

func (discardSink) Publish(string, string, Line) {}

// seal concatène les tranches d'une task en un objet unique, puis les purge.
//
// L'objet unique est ce qui rend la lecture par plage possible : l'interface
// n'affiche que la fin d'un journal de plusieurs dizaines de mégaoctets et remonte
// à la demande. Le faire sur une collection de tranches demanderait de les lister
// et de calculer des décalages à chaque requête.
func (c *Collector) seal(ctx context.Context, target Target) error {
	partsPrefix, err := c.storage.Keys.TaskLogPartsPrefix(target.RunName, target.TaskName)
	if err != nil {
		return err
	}

	chunks, err := c.storage.List(ctx, partsPrefix)
	if err != nil {
		return err
	}
	if len(chunks) == 0 {
		// Rien n'a été capté : task sans journal, ou déjà scellée. Les deux cas
		// sont normaux et ne justifient pas d'erreur.
		return nil
	}

	object, err := c.storage.Keys.TaskLogObject(target.RunName, target.TaskName)
	if err != nil {
		return err
	}

	// Les clés portent le rang de l'étape puis celui de la tranche, tous deux
	// zéro-paddés : un tri lexicographique suffit à rétablir la chronologie. Il
	// n'y a donc rien à savoir du pipeline pour assembler son journal.
	ordered := make([]string, 0, len(chunks))
	for _, chunk := range chunks {
		ordered = append(ordered, chunk.Key)
	}
	sortStrings(ordered)

	// Assemblage en mémoire tampon puis écriture d'un objet unique. Un tube vers
	// PutObject éviterait le tampon, mais empêcherait de connaître la taille — et
	// donc de la journaliser et de la vérifier.
	var assembled bytes.Buffer
	for _, key := range ordered {
		reader, err := c.storage.Get(ctx, key)
		if err != nil {
			return fmt.Errorf("lecture de la tranche %q: %w", key, err)
		}
		if _, err := io.Copy(&assembled, reader); err != nil {
			_ = reader.Close()
			return fmt.Errorf("assemblage de la tranche %q: %w", key, err)
		}
		_ = reader.Close()
	}

	if err := c.storage.Put(ctx, object,
		bytes.NewReader(assembled.Bytes()), int64(assembled.Len()), "text/plain; charset=utf-8"); err != nil {
		return err
	}

	// La base n'est mise à jour qu'après l'écriture de l'objet : l'ordre inverse
	// annoncerait un journal scellé que personne ne pourrait lire.
	if err := c.store.SealTaskLog(ctx, target.RunName, target.TaskName, object); err != nil {
		return err
	}

	// La purge vient en dernier, et son échec n'invalide pas le scellement : des
	// tranches orphelines coûtent du stockage, un journal manquant coûte une
	// investigation.
	for _, key := range ordered {
		if err := c.storage.Remove(ctx, key); err != nil {
			c.logger.Warn("purge d'une tranche", "objet", key, "error", err)
		}
	}

	c.logger.Info("journal scellé",
		"run", target.RunName, "task", target.TaskName, "objet", object,
		"octets", assembled.Len(), "tranches", len(ordered))
	return nil
}
