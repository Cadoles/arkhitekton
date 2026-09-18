package logs

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"sort"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/blob"
)

// DefaultTail est la quantité lue par défaut quand aucun décalage n'est demandé.
//
// Un journal d'intégration pèse des dizaines de mégaoctets : le charger en entier
// pour afficher la fin serait ruineux, et c'est la fin qu'on regarde en premier —
// l'échec est à la fin.
const DefaultTail int64 = 64 << 10

// MaxRead borne une lecture unitaire. Au-delà, l'appelant pagine par décalage.
const MaxRead int64 = 4 << 20

// Segment est une portion de journal.
type Segment struct {
	Content []byte
	// Offset est la position du premier octet rendu, dans le journal complet.
	Offset int64
	// NextOffset est la position à demander pour poursuivre. Égale à la taille
	// totale quand tout a été lu.
	NextOffset int64
	// Size est la taille totale connue à cet instant. Elle croît tant que la
	// task tourne.
	Size int64
	// Sealed dit que le journal est définitif : la taille ne bougera plus, et il
	// n'y a plus rien à attendre.
	Sealed bool
}

// Reader lit un journal, scellé ou en cours.
//
// Les deux cas sont servis par la même interface parce que l'appelant ne devrait
// pas avoir à savoir : un journal en cours est une suite de tranches, un journal
// scellé est un objet unique, et la position d'un octet dans le journal complet
// est la même dans les deux cas. C'est ce qui permet à un client de suivre un run
// en direct puis de continuer à lire après le scellement, sans rupture.
type Reader struct {
	storage *blob.Client
}

func NewReader(storage *blob.Client) *Reader {
	return &Reader{storage: storage}
}

// Tail rend la fin du journal.
func (r *Reader) Tail(ctx context.Context, runName, taskName string, length int64) (Segment, error) {
	if length <= 0 || length > MaxRead {
		length = DefaultTail
	}

	pieces, total, sealed, err := r.layout(ctx, runName, taskName)
	if err != nil {
		return Segment{}, err
	}

	offset := total - length
	if offset < 0 {
		offset = 0
	}
	return r.read(ctx, pieces, total, sealed, offset, length)
}

// From rend le journal à partir d'un décalage.
//
// C'est l'opération du suivi en direct : le client redemande à partir de
// `NextOffset` et reçoit ce qui s'est ajouté. Un décalage au-delà de la fin rend
// un segment vide plutôt qu'une erreur — la task n'a simplement rien produit
// depuis.
func (r *Reader) From(ctx context.Context, runName, taskName string, offset, length int64) (Segment, error) {
	if offset < 0 {
		offset = 0
	}
	if length <= 0 || length > MaxRead {
		length = MaxRead
	}

	pieces, total, sealed, err := r.layout(ctx, runName, taskName)
	if err != nil {
		return Segment{}, err
	}
	return r.read(ctx, pieces, total, sealed, offset, length)
}

// LinesBefore compte les lignes complètes qui précèdent un décalage.
//
// Sert à numéroter en absolu. La visionneuse n'ouvre que la fin d'un journal, et
// sans ce compte elle numérotait à partir de ce qu'elle avait chargé : le même
// « #L42 » désignait alors deux lignes différentes selon la quantité lue de part
// et d'autre d'un lien partagé.
//
// Le prix est assumé : compter demande de parcourir tout ce qui précède, donc de
// télécharger le début du journal que la lecture par plage évite justement. C'est
// pourquoi l'appel n'est fait qu'à l'ouverture d'une fenêtre, et jamais pendant
// le suivi — le flux n'ajoute qu'à la fin, ce qui ne renumérote rien.
//
// Un décalage nul ne coûte rien : le cas le plus courant, un journal plus court
// que la lecture par défaut, est donc gratuit.
func (r *Reader) LinesBefore(ctx context.Context, runName, taskName string, offset int64) (int64, error) {
	if offset <= 0 {
		return 0, nil
	}

	pieces, _, _, err := r.layout(ctx, runName, taskName)
	if err != nil {
		return 0, err
	}

	var count int64
	for _, p := range pieces {
		if p.start >= offset {
			break
		}
		lines, err := r.countNewlines(ctx, p.key, min(offset-p.start, p.size))
		if err != nil {
			return 0, err
		}
		count += lines
	}
	return count, nil
}

// countNewlines compte les sauts de ligne des `length` premiers octets d'un objet.
//
// La lecture se fait par tampon plutôt qu'en une fois : le début d'un journal
// d'intégration pèse des mégaoctets, et le charger entièrement en mémoire pour n'y
// compter que des octets serait gratuitement coûteux.
func (r *Reader) countNewlines(ctx context.Context, key string, length int64) (int64, error) {
	if length <= 0 {
		return 0, nil
	}

	reader, err := r.storage.GetRange(ctx, key, 0, length)
	if err != nil {
		return 0, fmt.Errorf("lecture de %q: %w", key, err)
	}
	defer reader.Close()

	var count int64
	buffer := make([]byte, 32<<10)
	for {
		n, err := reader.Read(buffer)
		count += int64(bytes.Count(buffer[:n], newline))
		if errors.Is(err, io.EOF) {
			return count, nil
		}
		if err != nil {
			return 0, fmt.Errorf("lecture de %q: %w", key, err)
		}
	}
}

var newline = []byte{'\n'}

// piece est un objet et sa position dans le journal complet.
type piece struct {
	key   string
	start int64
	size  int64
}

// layout établit la carte du journal.
//
// L'objet scellé prime sur les tranches : après scellement les tranches sont
// purgées, mais la purge peut échouer — elle ne remet pas le scellement en cause.
// Lire les deux produirait un journal en double.
func (r *Reader) layout(ctx context.Context, runName, taskName string) ([]piece, int64, bool, error) {
	object, err := r.storage.Keys.TaskLogObject(runName, taskName)
	if err != nil {
		return nil, 0, false, err
	}

	if info, err := r.storage.Stat(ctx, object); err == nil {
		return []piece{{key: object, start: 0, size: info.Size}}, info.Size, true, nil
	}

	prefix, err := r.storage.Keys.TaskLogPartsPrefix(runName, taskName)
	if err != nil {
		return nil, 0, false, err
	}

	chunks, err := r.storage.List(ctx, prefix)
	if err != nil {
		return nil, 0, false, err
	}
	if len(chunks) == 0 {
		return nil, 0, false, nil
	}

	// Les clés portent le rang de l'étape puis celui de la tranche, zéro-paddés :
	// l'ordre lexicographique est l'ordre du journal.
	sort.Slice(chunks, func(i, j int) bool { return chunks[i].Key < chunks[j].Key })

	pieces := make([]piece, 0, len(chunks))
	var total int64
	for _, chunk := range chunks {
		pieces = append(pieces, piece{key: chunk.Key, start: total, size: chunk.Size})
		total += chunk.Size
	}
	return pieces, total, false, nil
}

func (r *Reader) read(
	ctx context.Context, pieces []piece, total int64, sealed bool, offset, length int64,
) (Segment, error) {
	segment := Segment{Offset: offset, NextOffset: offset, Size: total, Sealed: sealed}

	if offset >= total {
		// Rien de nouveau. Le cas est normal en suivi : le client redemande plus
		// vite que la task n'écrit.
		segment.Offset = total
		segment.NextOffset = total
		return segment, nil
	}

	end := offset + length
	if end > total {
		end = total
	}

	content := make([]byte, 0, end-offset)
	for _, p := range pieces {
		pieceEnd := p.start + p.size
		if pieceEnd <= offset || p.start >= end {
			// Tranche entièrement hors de la fenêtre : ni lue, ni téléchargée.
			// C'est tout l'intérêt de la carte — afficher la fin d'un journal ne
			// doit pas coûter la lecture de son début.
			continue
		}

		from := max(offset-p.start, 0)
		to := min(end-p.start, p.size)

		chunk, err := r.readRange(ctx, p.key, from, to-from)
		if err != nil {
			return Segment{}, err
		}
		content = append(content, chunk...)
	}

	segment.Content = content
	segment.NextOffset = offset + int64(len(content))
	return segment, nil
}

func (r *Reader) readRange(ctx context.Context, key string, offset, length int64) ([]byte, error) {
	if length <= 0 {
		return nil, nil
	}

	reader, err := r.storage.GetRange(ctx, key, offset, length)
	if err != nil {
		return nil, fmt.Errorf("lecture de %q: %w", key, err)
	}
	defer reader.Close()

	content, err := io.ReadAll(reader)
	if err != nil {
		return nil, fmt.Errorf("lecture de %q: %w", key, err)
	}
	return content, nil
}
